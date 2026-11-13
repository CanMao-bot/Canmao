package ai

import (
	"context"
	"fmt"
	"strings"

	"gobot/core"
)

func qqActionIn(action, words string) bool {
	for _, word := range strings.Fields(words) {
		if word == action {
			return true
		}
	}
	return false
}

func qqCredentialAction(action string) bool {
	return qqActionIn(action, `get_cookies get_credentials get_clientkey get_csrf_token get_rkey get_rkey_server nc_get_rkey send_packet .handle_quick_operation`)
}

// Classify known queries explicitly. New/unknown APIs never inherit trust from
// their spelling (in particular the get_ prefix is not a safety boundary).
func riskForQQAction(action string) RiskLevel {
	action, err := canonicalQQAction(action)
	if err != nil {
		return RiskCritical
	}
	if _, known := qqCatalog()[action]; !known {
		return RiskCritical
	}
	if qqCredentialAction(action) || qqActionIn(action, `bot_exit set_restart clean_cache clean_stream_temp_file delete_friend set_group_leave set_group_admin set_group_add_option set_group_robot_add_option set_group_member_invite_policy set_group_member_permissions set_group_search set_group_new_member_history_visibility set_qq_avatar set_qq_profile set_self_longnick set_online_status set_diy_online_status set_qzone_ban set_qzone_msg_right set_qzone_profile test_auto_register_01 test_auto_register_02`) {
		return RiskCritical
	}
	if qqActionIn(action, `can_send_image can_send_record get_status get_version_info get_login_info get_friend_list get_friends_with_category get_unidirectional_friend_list get_group_list get_group_info get_group_info_ex get_group_detail_info get_group_member_info get_group_member_list get_group_honor_info get_group_shut_list get_group_signed_list get_group_at_all_remain get_essence_msg_list _get_group_notice get_msg get_forward_msg get_group_msg_history get_friend_msg_history get_recent_contact get_group_file_system_info get_group_root_files get_group_files_by_folder get_group_file_url get_private_file_url get_group_share_link get_share_link get_collection_list get_fileset_id get_fileset_info get_flash_file_list get_flash_file_url get_guild_list get_guild_service_profile get_online_clients get_stranger_info nc_get_user_status nc_get_packet_status get_robot_uin_range get_ai_characters get_ai_record get_profile_like get_emoji_likes fetch_emoji_like get_qun_album_list get_group_album_media_list get_qzone_profile get_qzone_msg_list get_qzone_msg_detail get_qzone_comment_list get_qzone_comment_replies get_qzone_feeds get_qzone_public_capabilities get_qzone_public_comments get_qzone_public_feed_detail get_qzone_public_feeds get_qzone_public_replies get_group_system_msg get_doubt_friends_add_request get_group_ignore_add_request get_group_ignored_notifies _get_model_show check_url_safely translate_en2zh`) {
		return RiskLow
	}
	if qqActionIn(action, `send_group_msg send_private_msg send_msg send_forward_msg send_group_forward_msg send_private_forward_msg send_like send_poke friend_poke group_poke mark_msg_as_read mark_group_msg_as_read mark_private_msg_as_read _mark_all_as_read set_input_status set_msg_emoji_like`) {
		return RiskMedium
	}
	return RiskHigh
}

func qqCallArgs(args map[string]interface{}) (string, map[string]interface{}, error) {
	name, _ := args["action"].(string)
	action, err := canonicalQQAction(name)
	if err != nil {
		return "", nil, err
	}
	params, ok := args["params"].(map[string]interface{})
	if !ok {
		return "", nil, fmt.Errorf("params 必须是 JSON 对象")
	}
	return action, params, nil
}

func (s *Service) authorizeQQCall(ctx context.Context, args map[string]interface{}) error {
	ev := core.EventFrom(ctx)
	if ev == nil || ev.UserID <= 0 {
		return errNoEvent
	}
	action, params, err := qqCallArgs(args)
	if err != nil {
		return err
	}
	owner := s.bot.Cfg.Bot.MasterID == fmtID(ev.UserID)
	if !owner && !isAdmin(s.bot, ev.UserID) {
		return errNoPermission("原生 QQ 接口仅主人/配置管理员可调用，成员请使用当前会话的专用工具")
	}
	if riskForQQAction(action) == RiskCritical && !owner {
		return errNoPermission("账号、凭据及未知 QQ 接口仅主人可发起")
	}
	if qqCredentialAction(action) && !ev.IsPrivate() {
		return errNoPermission("凭据和原始协议接口仅限主人私聊调用")
	}
	if _, known := qqCatalog()[action]; !known && !ev.IsPrivate() {
		return errNoPermission("未知 QQ 接口仅限主人私聊调用")
	}
	// Native file APIs can access the host outside Canmao's file sandbox.
	if !owner && (strings.Contains(action, "file") || strings.Contains(action, "stream") || qqActionIn(action, `get_image get_record ocr_image .ocr_image fetch_ptt_text set_group_portrait upload_image_to_qun_album upload_images_to_qun_album upload_video_to_qun_album`)) {
		return errNoPermission("原生文件/媒体接口仅主人可调用，管理员请使用有目录限制的文件工具")
	}
	if !owner && qqHasLocalPath(params) {
		return errNoPermission("原生 QQ 参数包含本机路径或 CQ 代码，仅主人可使用")
	}
	return s.authorizeQQTargets(action, params)
}

func qqHasLocalPath(v interface{}) bool {
	switch node := v.(type) {
	case string:
		x := strings.ToLower(strings.TrimSpace(node))
		return strings.Contains(x, "file:") || strings.Contains(x, "[cq:")
	case map[string]interface{}:
		for key, child := range node {
			if qqActionIn(strings.ToLower(key), "file path url thumb image cover avatar") {
				if text, ok := child.(string); ok && text != "" && !strings.HasPrefix(strings.ToLower(text), "https://") && !strings.HasPrefix(strings.ToLower(text), "http://") && !strings.HasPrefix(text, "base64://") {
					return true
				}
			}
			if qqHasLocalPath(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range node {
			if qqHasLocalPath(child) {
				return true
			}
		}
	}
	return false
}

func (s *Service) authorizeQQTargets(action string, params map[string]interface{}) error {
	if !qqActionIn(action, `set_group_ban set_group_kick set_group_kick_members .handle_quick_operation`) {
		return nil
	}
	if action == ".handle_quick_operation" {
		operation, _ := params["operation"].(map[string]interface{})
		kick, hasKick := operation["kick"]
		ban, hasBan := operation["ban"]
		if (!hasKick || kick == false) && (!hasBan || ban == false) {
			return nil
		}
	}
	var check func(interface{}) error
	check = func(v interface{}) error {
		switch node := v.(type) {
		case []interface{}:
			for _, item := range node {
				if err := check(item); err != nil {
					return err
				}
			}
		case map[string]interface{}:
			for key, item := range node {
				if key == "user_id" || key == "user_ids" || key == "members" {
					ids := []interface{}{item}
					if list, ok := item.([]interface{}); ok {
						ids = list
					}
					for _, id := range ids {
						uid := int64From(id)
						if uid > 0 && (fmtID(uid) == s.bot.Cfg.Bot.MasterID || uid == s.selfID() || isAdmin(s.bot, uid)) {
							return errNoPermission("不能禁言或移出主人、配置管理员或机器人自身")
						}
					}
				}
				if err := check(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check(params)
}
