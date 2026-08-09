// Package httpd 装配路由与监听器。
//
// 本包实现一条硬边界（FR-SYS-013 / NFR-SEC-030）：管理端**只在回环地址
// 上监听**。这不是靠中间件判断来源 IP 实现的——那种做法会被 X-Forwarded-For
// 之类的头绕过，也会因为一次路由注册疏忽而失效。此处直接开两个
// net.Listener：/admin 那一套只挂在 127.0.0.1 的监听器上，测评网络里
// 的手机在协议层就够不着它。
package httpd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type Server struct {
	// EvalHandler 面向测评网络（0.0.0.0），只含作答端路由。
	EvalHandler http.Handler
	// AdminHandler 只挂在回环监听器上。
	AdminHandler http.Handler
	// PortalHandler 是 HTTP:80 的连通性探测应答与跳转。
	PortalHandler http.Handler

	TLSConfig *tls.Config
	HTTPSPort int
	BindIP    string // 测评网络中的监听地址，空则 0.0.0.0
	Log       func(string, ...any)

	// AdminURL 在 Start 后填入，供启动横幅打印。
	AdminURL string

	servers []*http.Server
}

func (s *Server) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(f, a...)
	}
}

// Start 启动三个监听器。返回的 error 是致命的启动失败；
// 运行期错误通过 Log 输出。
func (s *Server) Start() error {
	// ① 作答端：全网可达，HTTPS
	evalAddr := fmt.Sprintf("%s:%d", s.BindIP, s.HTTPSPort)
	evalLn, err := net.Listen("tcp", evalAddr)
	if err != nil {
		return fmt.Errorf("绑定 %s 失败（端口被占用或权限不足）: %w", evalAddr, err)
	}
	evalSrv := &http.Server{
		Handler:           withSecurityHeaders(s.EvalHandler),
		TLSConfig:         s.TLSConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.servers = append(s.servers, evalSrv)
	go func() {
		if err := evalSrv.ServeTLS(evalLn, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logf("!! 作答端监听退出: %v", err)
		}
	}()
	s.logf("作答端  https://%s  （测评网络可达）", evalAddr)

	// ② 管理端：仅回环。协议层隔离，不依赖任何来源判断。
	adminAddr := fmt.Sprintf("127.0.0.1:%d", s.HTTPSPort)
	if s.BindIP == "127.0.0.1" || s.BindIP == "localhost" {
		// 开发机上作答端已经占了回环的同一端口，管理端换一个。
		adminAddr = fmt.Sprintf("127.0.0.1:%d", s.HTTPSPort+1)
	}
	adminLn, err := net.Listen("tcp", adminAddr)
	if err != nil {
		return fmt.Errorf("绑定管理端 %s 失败: %w", adminAddr, err)
	}
	adminSrv := &http.Server{
		Handler:           withSecurityHeaders(s.AdminHandler),
		TLSConfig:         s.TLSConfig,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.servers = append(s.servers, adminSrv)
	go func() {
		if err := adminSrv.ServeTLS(adminLn, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logf("!! 管理端监听退出: %v", err)
		}
	}()
	s.AdminURL = "https://" + adminAddr + "/admin/"
	s.logf("管理端  %s  （仅本机可达）", s.AdminURL)

	// ③ HTTP:80：连通性探测应答 + 跳转。
	// 绑定失败不致命——开发机上 80 端口常被占用，而探测应答只在
	// 现场组网时才需要。
	if s.PortalHandler != nil {
		portalSrv := &http.Server{
			Addr: ":80", Handler: s.PortalHandler, ReadHeaderTimeout: 10 * time.Second,
		}
		s.servers = append(s.servers, portalSrv)
		go func() {
			if err := portalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.logf("   HTTP:80 未启动（%v）——现场组网时必须可用，"+
					"否则手机会判定「无法上网」并切回移动数据", err)
			}
		}()
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) {
	for _, srv := range s.servers {
		_ = srv.Shutdown(ctx)
	}
}

func withSecurityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}
