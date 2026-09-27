// Package clientip определяет адрес клиента за балансировщиком (ADR-0016).
//
// X-Forwarded-For может подделать клиент: левую часть цепочки присылает он,
// правую дописывают наши прокси. Поэтому заголовок учитывается, только если
// соединение пришло от доверенного прокси, и читается справа налево до
// первого недоверенного адреса.
//
// IP — персональные данные: пакет кладёт его в контекст для лимитов и
// будущих нужд IAM, но в логи адрес не пишется.
package clientip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver знает доверенные прокси.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver создаёт Resolver. Пустой список — X-Forwarded-For не
// учитывается никогда.
func NewResolver(trusted []netip.Prefix) *Resolver {
	return &Resolver{trusted: trusted}
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP возвращает адрес клиента:
//  1. соединение не от доверенного прокси — его адрес, XFF игнорируется;
//  2. иначе записи X-Forwarded-For (все заголовки по порядку) справа
//     налево: доверенные пропускаются, первая недоверенная — клиент;
//  3. неразбираемая запись — граница доверия: клиентом считается последний
//     разобранный адрес справа от неё (доверенный прокси);
//  4. все записи доверенные — самая левая из них.
//
// ok = false, если адрес соединения не разобрать (не TCP).
func (r *Resolver) ClientIP(req *http.Request) (netip.Addr, bool) {
	peer, ok := parseAddr(req.RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	if !r.isTrusted(peer) {
		return peer, true
	}
	var hops []string
	for _, v := range req.Header.Values("X-Forwarded-For") {
		for h := range strings.SplitSeq(v, ",") {
			hops = append(hops, strings.TrimSpace(h))
		}
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			return client, true
		}
		a = a.Unmap()
		client = a
		if !r.isTrusted(a) {
			return a, true
		}
	}
	return client, true
}

func parseAddr(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// LimitKey — адрес для лимитов: IPv4 целиком, IPv6 по префиксу /64.
// Абоненту обычно выдаётся целая /64, и лимит по одному адресу обходится
// перебором адресов внутри неё.
func LimitKey(a netip.Addr) string {
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

type ctxKey struct{}

// WithIP кладёт адрес клиента в контекст.
func WithIP(ctx context.Context, a netip.Addr) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// FromContext возвращает адрес клиента из контекста.
func FromContext(ctx context.Context) (netip.Addr, bool) {
	a, ok := ctx.Value(ctxKey{}).(netip.Addr)
	return a, ok && a.IsValid()
}
