/**
 * @Author: fuxiao
 * @Email: 576101059@qq.com
 * @Date: 2021/8/27 11:31 上午
 * @Desc: TODO
 */

package core

import (
	"crypto/tls"
	"fmt"
	"math/rand"
	nethttp "net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/gu0213/http"
	"github.com/gu0213/tencent-im/internal/enum"
	"github.com/gu0213/tencent-im/internal/sign"
	"github.com/gu0213/tencent-im/internal/types"
)

const (
	defaultBaseUrl     = "https://adminapiger.im.qcloud.com"
	defaultVersion     = "v4"
	defaultContentType = "json"
	defaultExpiration  = 3600
)

var invalidResponse = NewError(enum.InvalidResponseCode, "invalid response")

type Client interface {
	// Get GET请求
	Get(serviceName string, command string, data interface{}, resp interface{}) error
	// Post POST请求
	Post(serviceName string, command string, data interface{}, resp interface{}) error
	// Put PUT请求
	Put(serviceName string, command string, data interface{}, resp interface{}) error
	// Patch PATCH请求
	Patch(serviceName string, command string, data interface{}, resp interface{}) error
	// Delete DELETE请求
	Delete(serviceName string, command string, data interface{}, resp interface{}) error
}

type client struct {
	client          *http.Client
	opt             *Options
	userSig         string
	userSigExpireAt int64
}

type Options struct {
	AppId      int    // 应用SDKAppID，可在即时通信 IM 控制台 的应用卡片中获取。
	AppSecret  string // 密钥信息，可在即时通信 IM 控制台 的应用详情页面中获取，具体操作请参见 获取密钥
	UserId     string // 用户ID
	Expiration int    // UserSig过期时间
	HTTP       *HTTPOptions
}

type HTTPOptions struct {
	Timeout             time.Duration
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
	DisableKeepAlives   bool
	// TraceLog 接收 HTTP 请求各阶段的诊断日志；为空时不启用跟踪。
	TraceLog func(format string, args ...interface{})
}

var traceRequestID uint64

// traceTransport 为每次 HTTP 请求附加阶段跟踪。
type traceTransport struct {
	base nethttp.RoundTripper
	// logf 将跟踪事件交给调用方的日志系统。
	logf func(format string, args ...interface{})
}

// RoundTrip 记录请求阶段并通过原有连接池发送请求。
func (t *traceTransport) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	started := time.Now()
	requestID := atomic.AddUint64(&traceRequestID, 1)
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			t.logf("im_http id=%d path=%s got_conn elapsed=%s reused=%t", requestID, req.URL.Path, time.Since(started), info.Reused)
		},
		ConnectDone: func(network, addr string, err error) {
			t.logf("im_http id=%d path=%s tcp_done elapsed=%s err=%v", requestID, req.URL.Path, time.Since(started), err)
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			t.logf("im_http id=%d path=%s tls_done elapsed=%s err=%v", requestID, req.URL.Path, time.Since(started), err)
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			t.logf("im_http id=%d path=%s request_written elapsed=%s err=%v", requestID, req.URL.Path, time.Since(started), info.Err)
		},
		GotFirstResponseByte: func() {
			t.logf("im_http id=%d path=%s first_byte elapsed=%s", requestID, req.URL.Path, time.Since(started))
		},
	}

	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := t.base.RoundTrip(req)
	t.logf("im_http id=%d path=%s round_trip_done elapsed=%s err=%v", requestID, req.URL.Path, time.Since(started), err)
	return resp, err
}

func NewClient(opt *Options, domain string) Client {
	rand.Seed(time.Now().UnixNano())
	c := new(client)
	c.opt = opt
	c.client = http.NewClient()

	if opt.HTTP != nil {
		transport := c.client.Transport.(*nethttp.Transport)
		transport.DisableKeepAlives = opt.HTTP.DisableKeepAlives
		transport.MaxIdleConns = opt.HTTP.MaxIdleConns
		transport.MaxIdleConnsPerHost = opt.HTTP.MaxIdleConnsPerHost
		transport.IdleConnTimeout = opt.HTTP.IdleConnTimeout
		c.client.SetTimeout(opt.HTTP.Timeout)
	}
	if opt.HTTP != nil && opt.HTTP.TraceLog != nil {
		c.client.Transport = &traceTransport{base: c.client.Transport, logf: opt.HTTP.TraceLog}
	}
	c.client.SetContentType(http.ContentTypeJson)
	c.client.SetBaseUrl(domain)
	return c
}

// Get GET请求
func (c *client) Get(serviceName string, command string, data interface{}, resp interface{}) error {
	return c.request(http.MethodGet, serviceName, command, data, resp)
}

// Post POST请求
func (c *client) Post(serviceName string, command string, data interface{}, resp interface{}) error {
	return c.request(http.MethodPost, serviceName, command, data, resp)
}

// Put PUT请求
func (c *client) Put(serviceName string, command string, data interface{}, resp interface{}) error {
	return c.request(http.MethodPut, serviceName, command, data, resp)
}

// Patch PATCH请求
func (c *client) Patch(serviceName string, command string, data interface{}, resp interface{}) error {
	return c.request(http.MethodPatch, serviceName, command, data, resp)
}

// Delete DELETE请求
func (c *client) Delete(serviceName string, command string, data interface{}, resp interface{}) error {
	return c.request(http.MethodDelete, serviceName, command, data, resp)
}

// request Request请求
func (c *client) request(method, serviceName, command string, data, resp interface{}) error {
	res, err := c.client.Request(method, c.buildUrl(serviceName, command), data)
	if err != nil {
		return err
	}

	if err = res.ScanBody(resp); err != nil {
		return err
	}

	if r, ok := resp.(types.ActionBaseRespInterface); ok {
		if r.GetActionStatus() == enum.FailActionStatus {
			return NewError(r.GetErrorCode(), r.GetErrorInfo())
		}

		if r.GetErrorCode() != enum.SuccessCode {
			return NewError(r.GetErrorCode(), r.GetErrorInfo())
		}
	} else if r, ok := resp.(types.BaseRespInterface); ok {
		if r.GetErrorCode() != enum.SuccessCode {
			return NewError(r.GetErrorCode(), r.GetErrorInfo())
		}
	} else {
		return invalidResponse
	}

	return nil
}

// buildUrl 构建一个请求URL
func (c *client) buildUrl(serviceName string, command string) string {
	format := "/%s/%s/%s?sdkappid=%d&identifier=%s&usersig=%s&random=%d&contenttype=%s"
	random := rand.Int31()
	userSig := c.getUserSig()
	return fmt.Sprintf(format, defaultVersion, serviceName, command, c.opt.AppId, c.opt.UserId, userSig, random, defaultContentType)
}

// getUserSig 获取签名
func (c *client) getUserSig() string {
	now, expiration := time.Now(), c.opt.Expiration

	if expiration <= 0 {
		expiration = defaultExpiration
	}

	if c.userSig == "" || c.userSigExpireAt <= now.Unix() {
		c.userSig, _ = sign.GenUserSig(c.opt.AppId, c.opt.AppSecret, c.opt.UserId, expiration)
		c.userSigExpireAt = now.Add(time.Duration(expiration) * time.Second).Unix()
	}

	return c.userSig
}
