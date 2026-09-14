package distlock

import "time"

// Option 配置 Lock 实例。
type Option func(*Lock)

// WithTTL 设置锁的过期时间。默认 30s。
// 看门狗会持续续期，因此业务耗时可以远大于 TTL；
// TTL 决定了"持有者彻底宕机后，锁多久自动释放"。
func WithTTL(d time.Duration) Option {
	return func(l *Lock) { l.ttl = d }
}

// WithRetryInterval 设置 Lock 轮询尝试加锁的间隔。默认 50ms。
func WithRetryInterval(d time.Duration) Option {
	return func(l *Lock) { l.retryInterval = d }
}

// WithWatchdog 开关自动续期。默认开启。
// 关闭后锁在 TTL 后无条件过期，业务耗时必须小于 TTL。
func WithWatchdog(enable bool) Option {
	return func(l *Lock) { l.watchdog = enable }
}

// WithOnLost 注册锁丢失回调（如续期发现锁不存在）。
// 回调最多触发一次，可用于告警、降级或终止依赖该锁的写操作。
func WithOnLost(fn func()) Option {
	return func(l *Lock) { l.onLost = fn }
}

// WithToken 覆盖默认的随机持有者标识。
// 业务可传入自增序号实现 fencing token：写数据时携带该 token，
// 由数据端校验，旧 token 的请求一律拒绝。
func WithToken(token string) Option {
	return func(l *Lock) { l.token = token }
}
