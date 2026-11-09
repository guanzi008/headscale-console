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
