package proxy

import (
	"github.com/kyokan/chaind/pkg"
	"time"
	"github.com/inconshreveable/log15"
	"github.com/kyokan/chaind/pkg/log"
	"fmt"
	"net/http"
	"errors"
	"strings"
		"sync/atomic"
	"encoding/json"
	"github.com/kyokan/chaind/pkg/config"
	"sync"
)

const ethCheckBody = "{\"jsonrpc\":\"2.0\",\"method\":\"eth_syncing\",\"params\":[],\"id\":%d}"

type BackendSwitch interface {
	pkg.Service
	BackendFor(t pkg.BackendType) (*config.Backend, error)
}

type BackendSwitchImpl struct {
	ethBackends []config.Backend
	currEth     int32
	quitChan    chan bool
	logger      log15.Logger
}

func NewBackendSwitch(backendCfg []config.Backend) BackendSwitch {
	var ethBackends []config.Backend
	var currEth int32

	for i, backend := range backendCfg {
		if backend.Type == pkg.EthBackend {
			ethBackends = append(ethBackends, backend)
		}

		if backend.Main {
			currEth = int32(i)
		}
	}

	return &BackendSwitchImpl{
		ethBackends: ethBackends,
		currEth:     currEth,
		quitChan:    make(chan bool),
		logger:      log.NewLog("proxy/backend_switch"),
	}
}

func (h *BackendSwitchImpl) Start() error {
	h.logger.Info("performing initial health checks on startup")
	h.performAllHealthchecks()

	go func() {
		tick := time.NewTicker(1 * time.Second)

		for {
			select {
			case <-tick.C:
				h.performAllHealthchecks()
			case <-h.quitChan:
				return
			}
		}
	}()

	return nil
}

func (h *BackendSwitchImpl) Stop() error {
	h.quitChan <- true
	return nil
}

func (h *BackendSwitchImpl) BackendFor(t pkg.BackendType) (*config.Backend, error) {
	var idx int32

	if t == pkg.EthBackend {
		idx = atomic.LoadInt32(&h.currEth)
	} else {
		return nil, errors.New("only Ethereum backends are supported")
	}

	if idx == -1 {
		return nil, errors.New("no backends available")
	}

	return &h.ethBackends[idx], nil
}

func (h *BackendSwitchImpl) performAllHealthchecks() {
	// use waitgroup so we can add btc checks later
	var wg sync.WaitGroup
	if h.currEth != -1 {
		wg.Add(1)
		go func() {
			idx := h.doHealthcheck(atomic.LoadInt32(&h.currEth), h.ethBackends)
			atomic.StoreInt32(&h.currEth, idx)
			wg.Done()
		}()
	}
	wg.Wait()
}

func (h *BackendSwitchImpl) doHealthcheck(idx int32, list []config.Backend) int32 {
	if idx == -1 {
		return -1
	}

	backend := list[idx]
	logger.Debug("performing healthcheck", "type", backend.Type, "name", backend.Name, "url", backend.URL)
	checker := NewChecker(&backend)
	ok := checker.Check()

	if !ok {
		logger.Warn("backend is unhealthy, trying another", "type", backend.Type, "name", backend.Name, "url", backend.URL)
		return h.doHealthcheck(h.nextBackend(idx, list))
	}

	logger.Debug("backend is ok", "type", backend.Type, "name", backend.Name, "url", backend.URL)
	return idx
}

func (h *BackendSwitchImpl) nextBackend(idx int32, list []config.Backend) (int32, []config.Backend) {
	backend := list[idx]
	if len(list) == 1 || idx == int32(len(list) - 1) {
		h.logger.Error("no more backends to try", "type", backend.Type)
		return -1, list
	}

	if idx < int32(len(list)-1) {
		return idx + 1, list
	}

	return 0, list
}

func NewChecker(backend *config.Backend) Checker {
	if backend.Type == pkg.EthBackend {
		return &ETHChecker{
			backend: backend,
			logger:  log.NewLog("proxy/eth_checker"),
		}
	}

	return nil
}

type Checker interface {
	Check() bool
}

type ETHChecker struct {
	backend *config.Backend
	logger  log15.Logger
}

func (e *ETHChecker) Check() bool {
	id := time.Now().Unix()
	data := fmt.Sprintf(ethCheckBody, id)
	client := &http.Client{
		Timeout: time.Duration(2 * time.Second),
	}
	res, err := client.Post(e.backend.URL, "application/json", strings.NewReader(data))
	if err != nil {
		e.logger.Warn("backend returned non-200 response", "name", e.backend.Name, "url", e.backend.URL)
		return false
	}
	defer res.Body.Close()
	var dec map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&dec)
	if err != nil {
		logger.Warn("backend returned invalid JSON", "name", e.backend.Name, "url", e.backend.URL)
		return false
	}
	if _, ok := dec["result"].(bool); !ok {
		logger.Warn("backend is either completing initial sync or has fallen behind", "name", e.backend.Name, "url", e.backend.URL)
		return false
	}
	return true
}
