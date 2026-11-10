package idp

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	LocaleEnglish = "en"
	LocaleChinese = "zh-CN"

	localeCookie = "hsc_locale"
)

var translations = map[string]map[string]string{
	LocaleEnglish: {
		"users.title":                    "Registered accounts",
		"users.description":              "Manage platform accounts. Each account owns its own private network.",
		"users.delete":                   "Delete account",
		"users.administrator":            "Platform administrator",
		"users.personal_network":         "Personal network owner",
		"users.disabled":                 "Disabled · deletion can be retried",
		"users.back":                     "Back to my account",
		"users.cancel":                   "Cancel",
		"users.delete_warning":           "This permanently deletes the account and its personal network, including devices, policies, DNS and keys. Existing sessions and third-party sign-ins will stop working. This cannot be undone.",
		"users.confirm_email":            "Type the account email to confirm",
		"users.confirm_mismatch":         "The confirmation email does not match.",
		"users.forbidden":                "Only platform administrators can manage registered accounts.",
		"users.not_found":                "This account no longer exists.",
		"users.self_delete":              "The current administrator and the last platform administrator cannot be deleted.",
		"users.load_failed":              "Could not load registered accounts.",
		"users.delete_failed":            "Could not finish deleting this account. Please retry.",
		"users.cleanup_failed":           "Network cleanup could not finish. A disabled account remains blocked from signing in; retry deletion after the service recovers.",
		"users.deleted":                  "The account and its personal network have been deleted.",
		"app.tagline":                    "Unified accounts and Tailnet console",
		"app.footer":                     "Protected by the built-in OIDC account center · Headplane + Headscale",
		"language.english":               "English",
		"language.chinese":               "中文",
		"common.email":                   "Email",
		"common.password":                "Password",
		"login.title":                    "Sign in to the account center",
		"login.description":              "After signing in, you'll return to Headplane or Headscale to continue authorization.",
		"login.submit":                   "Sign in",
		"login.or":                       "Or use a third-party account",
		"login.external_with":            "Sign in with %s",
		"login.create":                   "Create a new account",
		"register.title":                 "Create an account",
		"register.description":           "You'll be signed in automatically and can link a third-party account from the account page.",
		"register.display_name":          "Display name",
		"register.confirm_password":      "Confirm password",
		"register.submit":                "Create account and sign in",
		"register.existing":              "Already have an account? Sign in",
		"account.title":                  "Your account",
		"account.local_account":          "Local account",
		"account.logout":                 "Sign out",
		"account.third_party":            "Third-party sign-in",
		"account.linked":                 "Linked",
		"account.unlinked":               "Not linked",
		"account.unlink":                 "Unlink",
		"account.link":                   "Link",
		"account.no_providers":           "The administrator has not configured any third-party sign-in providers.",
		"account.change_password":        "Change password",
		"account.current_password":       "Current password",
		"account.new_password":           "New password",
		"account.confirm_new":            "Confirm new password",
		"account.update_password":        "Update password",
		"account.enter_headplane":        "Open Headplane",
		"account.network_title":          "Your private network",
		"account.network_description":    "You own this network. Its devices, access rules, DNS and keys are separate from other accounts.",
		"message.back_to_login":          "Back to sign in",
		"err.csrf_expired":               "This form has expired. Please try again.",
		"err.invalid_credentials":        "The email or password is incorrect.",
		"err.registration_closed":        "Registration is disabled",
		"err.registration_closed_detail": "The administrator has disabled account registration for this instance.",
		"err.email_invalid":              "Enter a valid email address.",
		"err.display_name_invalid":       "The display name must not be empty and must be at most 80 characters.",
		"err.password_min":               "The password must be at least %d characters.",
		"err.password_mismatch":          "The passwords do not match.",
		"err.email_domain":               "Registration is not allowed for this email domain.",
		"err.email_registered":           "This email address is already registered.",
		"err.unbound":                    "This third-party account is not linked yet. Sign in with your email and password first, then link it from the account page.",
		"err.state_expired":              "The third-party sign-in request has expired. Please try again.",
		"err.provider_unavailable":       "This third-party sign-in method is unavailable.",
		"err.oauth_not_found":            "Third-party sign-in unavailable",
		"err.oauth_not_found_detail":     "The requested third-party sign-in method was not found.",
		"err.oauth_denied":               "Third-party sign-in failed",
		"err.oauth_denied_detail":        "The third-party identity provider denied this sign-in.",
		"err.oauth_failed":               "Third-party sign-in failed",
		"err.oauth_failed_detail":        "The third-party account could not be verified. Check the server logs.",
		"err.oidc_invalid":               "Invalid OIDC request",
		"err.unknown_client":             "Unknown client_id.",
		"err.redirect_uri":               "The redirect_uri is not registered with the account center.",
		"err.current_password":           "The current password is incorrect.",
		"err.new_password_min":           "The new password must be at least %d characters.",
		"err.new_password_mismatch":      "The new passwords do not match.",
		"err.provider_unknown":           "Unknown third-party sign-in method.",
		"err.link_failed":                "This third-party account could not be linked.",
		"err.link_conflict":              "This third-party account is already linked, or your account already has a link of this type.",
		"notice.password_updated":        "Password updated.",
		"notice.unlinked":                "Third-party account unlinked.",
		"notice.linked":                  "Third-party account linked.",
	},
	LocaleChinese: {
		"users.title":                    "注册账号管理",
		"users.description":              "管理平台注册账号。每个账号拥有独立的私人网络。",
		"users.delete":                   "删除账号",
		"users.administrator":            "平台管理员",
		"users.personal_network":         "个人网络所有者",
		"users.disabled":                 "已停用 · 可重试删除",
		"users.back":                     "返回我的账号",
		"users.cancel":                   "取消",
		"users.delete_warning":           "此操作将永久删除该账号及其个人网络，包括设备、策略、DNS 和密钥。已有登录会话和第三方登录将失效，删除后无法恢复。",
		"users.confirm_email":            "输入该账号的邮箱以确认删除",
		"users.confirm_mismatch":         "确认邮箱与目标账号不一致。",
		"users.forbidden":                "只有平台管理员可以管理注册账号。",
		"users.not_found":                "此账号已不存在。",
		"users.self_delete":              "不能删除当前管理员或最后一位平台管理员。",
		"users.load_failed":              "无法加载注册账号。",
		"users.delete_failed":            "账号删除尚未完成，请重试。",
		"users.cleanup_failed":           "网络清理未完成。已停用的账号会继续禁止登录，请在服务恢复后重试删除。",
		"users.deleted":                  "账号及其个人网络已删除。",
		"app.tagline":                    "统一账号与 Tailnet 控制台",
		"app.footer":                     "由内置 OIDC 账号中心保护 · Headplane + Headscale",
		"language.english":               "English",
		"language.chinese":               "中文",
		"common.email":                   "邮箱",
		"common.password":                "密码",
		"login.title":                    "登录账号中心",
		"login.description":              "登录后将自动返回 Headplane 或 Headscale 继续授权。",
		"login.submit":                   "登录",
		"login.or":                       "或使用第三方账号",
		"login.external_with":            "使用 %s 登录",
		"login.create":                   "创建新账号",
		"register.title":                 "创建账号",
		"register.description":           "账号创建后会自动登录，并可在账号页面绑定第三方登录。",
		"register.display_name":          "显示名称",
		"register.confirm_password":      "确认密码",
		"register.submit":                "创建并登录",
		"register.existing":              "已有账号，返回登录",
		"account.title":                  "你的账号",
		"account.local_account":          "本地账号",
		"account.logout":                 "退出",
		"account.third_party":            "第三方登录",
		"account.linked":                 "已绑定",
		"account.unlinked":               "未绑定",
		"account.unlink":                 "解绑",
		"account.link":                   "绑定",
		"account.no_providers":           "管理员尚未配置第三方登录提供方。",
		"account.change_password":        "修改密码",
		"account.current_password":       "当前密码",
		"account.new_password":           "新密码",
		"account.confirm_new":            "确认新密码",
		"account.update_password":        "更新密码",
		"account.enter_headplane":        "进入 Headplane",
		"account.network_title":          "我的私有网络",
		"account.network_description":    "你是此网络的所有者。设备、访问规则、DNS 和密钥均独立于其他账号。",
		"message.back_to_login":          "返回登录",
		"err.csrf_expired":               "表单已过期，请重试。",
		"err.invalid_credentials":        "邮箱或密码不正确。",
		"err.registration_closed":        "注册已关闭",
		"err.registration_closed_detail": "管理员已关闭此实例的账号注册。",
		"err.email_invalid":              "请输入有效的邮箱地址。",
		"err.display_name_invalid":       "显示名称不能为空且不能超过 80 个字符。",
		"err.password_min":               "密码至少需要 %d 个字符。",
		"err.password_mismatch":          "两次输入的密码不一致。",
		"err.email_domain":               "该邮箱域名不允许注册。",
		"err.email_registered":           "该邮箱已注册。",
		"err.unbound":                    "该第三方账号尚未绑定。请先使用邮箱和密码登录，再到账号页面绑定。",
		"err.state_expired":              "第三方登录请求已过期，请重试。",
		"err.provider_unavailable":       "第三方登录方式不可用。",
		"err.oauth_not_found":            "第三方登录不可用",
		"err.oauth_not_found_detail":     "未找到该第三方登录方式。",
		"err.oauth_denied":               "第三方登录失败",
		"err.oauth_denied_detail":        "第三方身份提供方拒绝了本次登录。",
		"err.oauth_failed":               "第三方登录失败",
		"err.oauth_failed_detail":        "无法验证第三方账号，请检查服务端日志。",
		"err.oidc_invalid":               "OIDC 请求无效",
		"err.unknown_client":             "未知的 client_id。",
		"err.redirect_uri":               "redirect_uri 未在账号中心注册。",
		"err.current_password":           "当前密码不正确。",
		"err.new_password_min":           "新密码至少需要 %d 个字符。",
		"err.new_password_mismatch":      "两次输入的新密码不一致。",
		"err.provider_unknown":           "未知的第三方登录方式。",
		"err.link_failed":                "无法绑定该第三方账号。",
		"err.link_conflict":              "该第三方账号已经绑定，或者你的账号已经绑定了同类型的第三方账号。",
		"notice.password_updated":        "密码已更新。",
		"notice.unlinked":                "第三方账号已解除绑定。",
		"notice.linked":                  "第三方账号已绑定。",
	},
}

func normalizeLocale(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.HasPrefix(value, "zh"):
		return LocaleChinese
	case strings.HasPrefix(value, "en"):
		return LocaleEnglish
	default:
		return ""
	}
}

func requestLocale(r *http.Request) string {
	if locale := normalizeLocale(r.URL.Query().Get("lang")); locale != "" {
		return locale
	}
	if cookie, err := r.Cookie(localeCookie); err == nil {
		if locale := normalizeLocale(cookie.Value); locale != "" {
			return locale
		}
	}
	if locale := localeFromAcceptLanguage(r.Header.Get("Accept-Language")); locale != "" {
		return locale
	}
	return LocaleEnglish
}

func localeFromAcceptLanguage(header string) string {
	bestLocale := ""
	bestQuality := -1.0
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		locale := normalizeLocale(tag)
		if locale == "" {
			continue
		}
		quality := 1.0
		if params != "" {
			key, value, ok := strings.Cut(strings.TrimSpace(params), "=")
			if ok && key == "q" {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil {
					continue
				}
				quality = parsed
			}
		}
		if quality > bestQuality {
			bestLocale = locale
			bestQuality = quality
		}
	}
	if bestQuality <= 0 {
		return ""
	}
	return bestLocale
}

func translate(locale, key string, args ...any) string {
	messages, ok := translations[locale]
	if !ok {
		messages = translations[LocaleEnglish]
	}
	text, ok := messages[key]
	if !ok {
		text = translations[LocaleEnglish][key]
	}
	if text == "" {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(text, args...)
	}
	return text
}
