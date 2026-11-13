// Static extraction only: never loads or executes the NapCat application.
// Setup: npm install --prefix .local.tools acorn@8.15.0
// Usage: node scripts/extract_napcat_catalog.cjs /path/to/napcat.mjs [output.json]
const fs = require('node:fs');
const path = require('node:path');
let acorn;
try { acorn = require('acorn'); } catch { acorn = require('../.local.tools/node_modules/acorn'); }
const source = fs.readFileSync(process.argv[2], 'utf8');
const ast = acorn.parse(source, {ecmaVersion: 'latest', sourceType: 'module'});
const bindings = new Map(), classes = new Map(), functions = new Map();
for (const node of ast.body) {
  if (node.type === 'VariableDeclaration') for (const item of node.declarations) if (item.id.type === 'Identifier') {
    bindings.set(item.id.name, item.init);
    if (item.init?.type === 'ClassExpression') classes.set(item.id.name, item.init);
  }
  if (node.type === 'ClassDeclaration') classes.set(node.id.name, node);
  if (node.type === 'FunctionDeclaration') functions.set(node.id.name, node);
}
const unsupported = new Set();
function value(node, seen = new Set()) {
  if (!node) return undefined;
  if (node.type === 'Literal') return node.value;
  if (node.type === 'Identifier') {
    if (seen.has(node.name) || seen.size > 30) return undefined;
    return value(bindings.get(node.name), new Set([...seen, node.name]));
  }
  if (node.type === 'UnaryExpression') {
    const v = value(node.argument, seen);
    if (node.operator === '!') return !v;
    if (node.operator === '-') return -v;
  }
  if (node.type === 'BinaryExpression') {
    const a = value(node.left, seen), b = value(node.right, seen);
    if (node.operator === '*') return a * b;
    if (node.operator === '+') return a + b;
  }
  if (node.type === 'ArrayExpression') return node.elements.map(item => value(item, seen));
  if (node.type === 'ObjectExpression') {
    const result = {};
    for (const prop of node.properties) {
      if (prop.type === 'SpreadElement') Object.assign(result, value(prop.argument, seen));
      else result[prop.key.name ?? prop.key.value] = value(prop.value, seen);
    }
    return result;
  }
  if (node.type === 'MemberExpression') return value(node.object, seen)?.[node.property.name ?? node.property.value];
  if (node.type === 'CallExpression' && node.callee.type === 'MemberExpression') {
    const name = node.callee.property.name;
    const args = node.arguments.map(arg => value(arg, seen));
    const options = args[args.length - 1];
    const opt = options && typeof options === 'object' && !Array.isArray(options) ? options : {};
    if (name === 'String') return {type: 'string', ...(args[0] || {})};
    if (name === 'Number' || name === 'Integer') return {type: name.toLowerCase(), ...(args[0] || {})};
    if (name === 'Boolean') return {type: 'boolean', ...(args[0] || {})};
    if (name === 'Null') return {type: 'null', ...(args[0] || {})};
    if (name === 'Any' || name === 'Unknown') return {...(args[0] || {})};
    if (name === 'Undefined') return {};
    if (name === 'Enum') {
      const members = [...new Set(Object.values(args[0] || {}).filter(v => typeof v === 'string' || typeof v === 'number'))];
      return members.length ? {enum: members, ...(args[1] || {})} : {};
    }
    if (name === 'Literal') return {const: args[0], ...(args[1] || {})};
    if (name === 'Optional') return {...(args[0] || {}), __optional: true};
    if (name === 'Readonly') return args[0];
    if (name === 'Array') return {type: 'array', items: args[0] || {}, ...(args[1] || {})};
    if (name === 'Union') return {anyOf: (args[0] || []).map(v => v || {}), ...(args[1] || {})};
    if (name === 'Intersect') return {allOf: args[0] || [], ...(args[1] || {})};
    if (name === 'Object') {
      const properties = args[0] || {};
      const required = Object.entries(properties).filter(([, v]) => v && !v.__optional && v.default === undefined).map(([k]) => k);
      return {type: 'object', properties, ...(required.length ? {required} : {}), ...(args[1] || {})};
    }
    if (name === 'Record') return {type: 'object', additionalProperties: args[1] || {}, ...(args[2] || {})};
    if (name === 'Partial') {
      const result = {...args[0], ...(args[1] || {})};
      delete result.required;
      return result;
    }
    if (name === 'Omit') {
      const omitted = new Set(args[1] || []);
      return {...args[0], properties: Object.fromEntries(Object.entries(args[0]?.properties || {}).filter(([k]) => !omitted.has(k))), required: (args[0]?.required || []).filter(k => !omitted.has(k)), ...(args[2] || {})};
    }
    unsupported.add(name);
    return {};
  }
  return undefined;
}
function fields(node, seen = new Set()) {
  if (!node || seen.has(node)) return {};
  const result = fields(classes.get(node.superClass?.name), new Set([...seen, node]));
  for (const prop of node.body.body) if (prop.type === 'PropertyDefinition' && !prop.static) result[prop.key.name] = prop.value;
  return result;
}
const actions = new Map();
function walk(node, fn) {
  if (!node || typeof node !== 'object') return;
  if (node.type) fn(node);
  for (const child of Object.values(node)) {
    if (Array.isArray(child)) for (const item of child) walk(item, fn);
    else if (child && typeof child === 'object') walk(child, fn);
  }
}
let registered = new Set();
for (const fn of functions.values()) {
  const found = new Set();
  walk(fn.body, node => {if (node.type === 'NewExpression' && node.callee.type === 'Identifier' && classes.has(node.callee.name)) found.add(node.callee.name);});
  if (found.size > registered.size) registered = found;
}
for (const node of ast.body) {
  const expr = node.type === 'ExpressionStatement' && node.expression;
  if (expr?.type === 'AssignmentExpression' && expr.right.type === 'CallExpression' && expr.right.arguments[0]?.type === 'ArrayExpression' && expr.right.arguments[0].elements.some(v => v?.name === 'eU')) registered.add(expr.left.name);
}
for (const [name, node] of classes) {
  const f = fields(node);
  const action = value(f.actionName);
  if (typeof action !== 'string' || !action || action === 'unknown') continue;
  // Include classes actually registered in the action factory, including auto registration.
  if (!registered.has(name)) continue;
  const summary = value(f.actionSummary) || action;
  const parameters = value(f.payloadSchema) || {type: 'object'};
  actions.set(action, {action, summary, description: value(f.actionDescription) || '', tags: value(f.actionTags) || [], parameters, stream: value(f.useStream) === true});
}
function clean(obj) {
  if (Array.isArray(obj)) return obj.map(clean);
  if (!obj || typeof obj !== 'object') return obj;
  return Object.fromEntries(Object.entries(obj).filter(([k]) => k !== '__optional').map(([k, v]) => [k, clean(v)]));
}
const result = {source: 'NapCat OneBot action metadata', actions: clean([...actions.values()].sort((a,b) => a.action.localeCompare(b.action)))};
const output = process.argv[3] || path.join(__dirname, '../services/ai/qq_actions.json');
fs.writeFileSync(output, JSON.stringify(result, null, 2) + '\n');
console.log(JSON.stringify({actions: result.actions.length, schemas: result.actions.filter(a => Object.keys(a.parameters.properties || {}).length).length, unsupportedSchemaBuilders: [...unsupported].sort()}));
