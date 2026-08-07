#!/usr/bin/env python3
"""Playwright 无头浏览器脚本: 供 gobot 调用
用法: python3 browser.py <action> <arg>
action:
  open <url> [wait_ms]         打开网页并提取正文文本
  screenshot <url> <outfile>   截图保存为 PNG
  exec <url> <js>              打开页面执行 JS 返回结果
"""
import json
import sys
import time
from playwright.sync_api import sync_playwright

def extract_text(page):
    # 提取可见文本
    text = page.evaluate("""() => {
        const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        const parts = [];
        let n;
        while (n = walker.nextNode()) {
            const t = n.textContent.trim();
            if (t) parts.push(t);
        }
        return parts.join('\\n').slice(0, 8000);
    }""")
    title = page.title()
    return f"标题: {title}\n\n{text}"

def main():
    action = sys.argv[1] if len(sys.argv) > 1 else "open"
    with sync_playwright() as p:
        browser = p.chromium.launch(
            headless=True,
            args=["--no-sandbox", "--disable-dev-shm-usage"]
        )
        page = browser.new_page(user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
        try:
            if action == "open":
                url = sys.argv[2]
                wait = int(sys.argv[3]) if len(sys.argv) > 3 else 3000
                page.goto(url, wait_until="domcontentloaded", timeout=30000)
                time.sleep(wait / 1000)
                print(extract_text(page))
            elif action == "screenshot":
                url = sys.argv[2]
                out = sys.argv[3]
                page.goto(url, wait_until="domcontentloaded", timeout=30000)
                time.sleep(2000/1000)
                page.screenshot(path=out, full_page=True)
                print(json.dumps({"ok": True, "file": out}))
            elif action == "exec":
                url = sys.argv[2]
                js = sys.argv[3]
                page.goto(url, wait_until="domcontentloaded", timeout=30000)
                time.sleep(1500/1000)
                result = page.evaluate(js)
                print(str(result)[:8000])
            else:
                print(json.dumps({"error": f"unknown action {action}"}))
        except Exception as e:
            print(json.dumps({"error": str(e)}))
        finally:
            browser.close()

if __name__ == "__main__":
    main()
