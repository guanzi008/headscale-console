package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type Router struct {
	adminPath string
	idp       http.Handler
	headplane http.Handler
	headscale http.Handler
}

func New(adminPath string, idpURL, headplaneURL, headscaleURL *url.URL, logger *slog.Logger) *Router {
	return &Router{
		adminPath: adminPath,
		idp:       reverseProxy("idp", idpURL, logger),
		headplane: reverseProxy("headplane", headplaneURL, logger),
		headscale: reverseProxy("headscale", headscaleURL, logger),
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	switch {
	case pathWithin(request.URL.Path, "/idp"):
		r.idp.ServeHTTP(w, request)
	case pathWithin(request.URL.Path, r.adminPath):
		r.headplane.ServeHTTP(w, request)
	case request.URL.Path == "/":
		http.Redirect(w, request, r.adminPath+"/", http.StatusTemporaryRedirect)
	default:
		r.headscale.ServeHTTP(w, request)
	}
}

func reverseProxy(name string, target *url.URL, logger *slog.Logger) *httputil.ReverseProxy {
	if logger == nil {
		logger = slog.Default()
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalHost := request.Host
		director(request)
		request.Header.Set("X-Forwarded-Host", originalHost)
		if request.TLS != nil {
			request.Header.Set("X-Forwarded-Proto", "https")
		} else if request.Header.Get("X-Forwarded-Proto") == "" {
			request.Header.Set("X-Forwarded-Proto", "http")
		}
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, request *http.Request, err error) {
		logger.Warn("upstream request failed", "service", name, "path", request.URL.Path, "error", err)
		http.Error(w, "服务暂时不可用，请稍后重试。", http.StatusBadGateway)
	}
	return proxy
}

func pathWithin(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}
