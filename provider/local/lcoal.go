package provider

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sagernet/fswatch"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/provider"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/provider/parser"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
	"github.com/sagernet/sing/service/pause"
)

func RegisterProvider(registry *provider.Registry) {
	provider.Register[option.ProviderLocalOptions](registry, C.ProviderTypeLocal, NewProviderLocal)
}

var (
	_ adapter.Provider = (*ProviderLocal)(nil)
	_ adapter.Service  = (*ProviderLocal)(nil)
)

type ProviderLocal struct {
	provider.Adapter
	ctx         context.Context
	cancel      context.CancelFunc
	logger      log.ContextLogger
	outbound    adapter.OutboundManager
	lastOutOpts []option.Outbound
	lastUpdated time.Time
	watcher     *fswatch.Watcher
	updating    atomic.Bool
}

func NewProviderLocal(ctx context.Context, router adapter.Router, logFactory log.Factory, tag string, options option.ProviderLocalOptions) (adapter.Provider, error) {
	if tag == "" {
		return nil, E.New("provider tag is required")
	}
	if options.Path == "" {
		return nil, E.New("provider path is required")
	}
	var (
		outbound     = service.FromContext[adapter.OutboundManager](ctx)
		pauseManager = service.FromContext[pause.Manager](ctx)
		logger       = logFactory.NewLogger(F.ToString("provider/local", "[", tag, "]"))
	)
	provider := &ProviderLocal{
		Adapter: provider.NewAdapter(ctx, router, outbound, pauseManager, logFactory, logger, tag, C.ProviderTypeLocal, options.HealthCheck),
		ctx:     ctx,
		logger:  logger,
	}
	filePath := filemanager.BasePath(ctx, options.Path)
	filePath, _ = filepath.Abs(filePath)
	err := provider.reloadFile(filePath)
	if err != nil {
		return nil, err
	}
	watcher, err := fswatch.NewWatcher(fswatch.Options{
		Path: []string{filePath},
		Callback: func(path string) {
			if provider.updating.Swap(true) {
				return
			}
			defer func() {
				provider.updating.Store(false)
				provider.UpdateGroups()
			}()
			uErr := provider.reloadFile(path)
			if uErr != nil {
				logger.Error(E.Cause(uErr, "reload provider ", tag))
			}
		},
	})
	if err != nil {
		return nil, err
	}
	provider.watcher = watcher
	return provider, nil
}

func (s *ProviderLocal) Start() error {
	err := s.Adapter.Start()
	if err != nil {
		return err
	}
	if s.watcher != nil {
		err := s.watcher.Start()
		if err != nil {
			s.logger.Error(E.Cause(err, "watch provider file"))
		}
	}
	return nil
}

func (s *ProviderLocal) UpdatedAt() time.Time {
	return s.lastUpdated
}

func (s *ProviderLocal) reloadFile(path string) error {
	if fileInfo, err := os.Stat(path); err == nil {
		s.lastUpdated = fileInfo.ModTime()
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	outboundOpts, err := parser.ParseSubscription(s.ctx, string(content))
	if err != nil {
		return err
	}
	s.UpdateOutbounds(s.lastOutOpts, outboundOpts)
	s.lastOutOpts = outboundOpts
	return nil
}

func (s *ProviderLocal) IsUpdating() bool {
	return s.updating.Load()
}

func (s *ProviderLocal) Close() error {
	return common.Close(&s.Adapter, common.PtrOrNil(s.watcher))
}
