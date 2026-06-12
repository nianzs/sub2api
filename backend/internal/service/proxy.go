package service

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

const (
	FallbackModeNone   = "none"
	FallbackModeProxy  = "proxy"
	FallbackModeDirect = "direct"
)

// DefaultProxyMaxAccounts 是 proxy 创建时 max_accounts 的默认值。
// 取 3 是 Kiro 风控经验值：同 IP 绑过多账号会触发批量封禁。
const DefaultProxyMaxAccounts = 3

// ProxyValidationError 表示新建/更新账号时代理选择不合规的错误。
// Handler 应将其映射为 HTTP 422 + machine-readable reason。
type ProxyValidationError struct {
	Reason  string // 机器可读，如 kiro_account_requires_proxy / proxy_capacity_full
	Message string // 人类可读，可选
}

const (
	ProxyValidationKiroRequiresProxy = "kiro_account_requires_proxy"
	ProxyValidationProxyNotFound     = "proxy_not_found"
	ProxyValidationProxyNotActive    = "proxy_not_active"
	ProxyValidationProxyExpired      = "proxy_expired"
	ProxyValidationCapacityFull      = "proxy_capacity_full"
)

func (e *ProxyValidationError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("proxy_validation_failed: %s — %s", e.Reason, e.Message)
	}
	return fmt.Sprintf("proxy_validation_failed: %s", e.Reason)
}

type Proxy struct {
	ID             int64
	Name           string
	Protocol       string
	Host           string
	Port           int
	Username       string
	Password       string
	Status         string
	MaxAccounts    int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      *time.Time
	FallbackMode   string
	BackupProxyID  *int64
	ExpiryWarnDays int
}

func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

// IsExpired 报告代理是否已过期（基于 expires_at，与 status 无关）。
func (p *Proxy) IsExpired(now time.Time) bool {
	return p.ExpiresAt != nil && !p.ExpiresAt.After(now)
}

// HasCapacity 报告当前绑定账号数是否还在 max_accounts 之内。
// MaxAccounts<=0 视为无限制（仅兼容历史数据）。
func (p *Proxy) HasCapacity(currentCount int64) bool {
	if p == nil {
		return false
	}
	if p.MaxAccounts <= 0 {
		return true
	}
	return currentCount < int64(p.MaxAccounts)
}

// proxyIDPtrEqual 判断两个 *int64 是否相等，两个 nil 视为相等。
func proxyIDPtrEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func (p *Proxy) URL() string {
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
	}
	if p.Username != "" && p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

type ProxyWithAccountCount struct {
	Proxy
	AccountCount   int64
	LatencyMs      *int64
	LatencyStatus  string
	LatencyMessage string
	IPAddress      string
	Country        string
	CountryCode    string
	Region         string
	City           string
	QualityStatus  string
	QualityScore   *int
	QualityGrade   string
	QualitySummary string
	QualityChecked *int64
}

type ProxyAccountSummary struct {
	ID       int64
	Name     string
	Platform string
	Type     string
	Notes    *string
}
