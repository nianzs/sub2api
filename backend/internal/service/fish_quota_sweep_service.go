package service

import (
	"context"
	"log"
	"sync"
	"time"
)

// fishUsageProber 抽象 fish 用量探测（*AccountUsageService 实现，测试可替换）。
// GetUsageForAccount 自带 3min 成功缓存 / 1min 错误负缓存 / singleflight /
// 按账号代理路由；探测的冷却与 plan 回填副作用在用量拉取内部完成
// （applyFishDailyObservation），巡检只负责周期性触发。
type fishUsageProber interface {
	GetUsageForAccount(ctx context.Context, account *Account, force ...bool) (*UsageInfo, error)
}

// fishQuotaSweepConcurrency 周期巡检的并发度（镜像 cnQuotaProbeConcurrency）。
const fishQuotaSweepConcurrency = 4

// FishQuotaSweepService 周期巡检 fish token 号的每日出图窗口：看到次数耗尽
// 即落临时不可调度到重置时刻（resetAt 缺失/过期兜底 UTC 零点）。这是请求
// 通道（402 反应式）与观察通道（管理端看用量）之外的第三通道——没被流量
// 打到、也没人看列表的耗尽号同样会挂上冷却。
//
// 克隆自 CNProviderBalanceCheckService 的 Start/Stop/runOnce + ticker 骨架。
// 不做「健康即恢复」清除：冷却自然在重置时刻过期，SetTempUnschedulable 只
// 延长，巡检不触碰其他子系统写入的冷却。
type FishQuotaSweepService struct {
	prober      fishUsageProber
	accountRepo AccountRepository
	interval    time.Duration
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

// NewFishQuotaSweepService 构造周期配额巡检服务。
// interval <= 0 时 Start() 直接返回（不启动），便于通过配置关闭。
func NewFishQuotaSweepService(
	accountRepo AccountRepository,
	usage *AccountUsageService,
	interval time.Duration,
) *FishQuotaSweepService {
	svc := &FishQuotaSweepService{
		accountRepo: accountRepo,
		interval:    interval,
		stopCh:      make(chan struct{}),
	}
	if usage != nil {
		svc.prober = usage
	}
	return svc
}

func (s *FishQuotaSweepService) Start() {
	if s == nil || s.accountRepo == nil || s.prober == nil {
		return
	}
	if s.interval <= 0 {
		return
	}
	log.Printf("[FishQuotaSweep] started (interval=%s)", s.interval)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		// 启动后先等待一个周期再首次巡检，避免与进程启动峰重叠。
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *FishQuotaSweepService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *FishQuotaSweepService) runOnce() {
	accounts, err := s.accountRepo.ListByPlatform(context.Background(), PlatformFish)
	if err != nil {
		log.Printf("[FishQuotaSweep] list fish accounts failed: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.runOnceBudget(len(accounts)))
	defer cancel()

	sem := make(chan struct{}, fishQuotaSweepConcurrency)
	var wg sync.WaitGroup
	for i := range accounts {
		account := &accounts[i]
		// 中转号没有 fish entitlements；非 active 号（error/手动禁用）不必巡检。
		if !account.IsActive() || !isFishTokenAccount(account) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, err := s.prober.GetUsageForAccount(ctx, account); err != nil {
				log.Printf("[FishQuotaSweep] probe account %d failed: %v", account.ID, err)
			}
		}()
	}
	wg.Wait()
}

// runOnceBudget 单轮预算：每账号 10s（含代理握手），下限 30s。
func (s *FishQuotaSweepService) runOnceBudget(accountCount int) time.Duration {
	budget := 30*time.Second + time.Duration(accountCount)*10*time.Second
	if budget > 5*time.Minute {
		budget = 5 * time.Minute
	}
	return budget
}
