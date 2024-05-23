package web

import (
	"context"
	"crypto/tls"
	"emperror.dev/errors"
	"fmt"
	"github.com/bluele/gcache"
	"github.com/gin-gonic/gin"
	mediaserveractionproto "github.com/je4/mediaserverproto/v2/pkg/mediaserveraction/proto"
	mediaserverdbproto "github.com/je4/mediaserverproto/v2/pkg/mediaserverdb/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func NewController(addr, extAddr string, tlsConfig *tls.Config, dbClient mediaserverdbproto.DBControllerClient, actionControllerClient mediaserveractionproto.ActionControllerClient, logger zLogger.ZLogger) (*controller, error) {
	u, err := url.Parse(extAddr)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid external address '%s'", extAddr)
	}
	subpath := "/" + strings.Trim(u.Path, "/")

	router := gin.Default()

	c := &controller{
		addr:                   addr,
		router:                 router,
		subpath:                subpath,
		logger:                 logger,
		dbClient:               dbClient,
		actionControllerClient: actionControllerClient,
		actionParams:           map[string][]string{},
		itemCache: gcache.New(200).
			LRU().Expiration(10 * time.Minute).
			LoaderFunc(func(key any) (any, error) {

			}).
			Build(),
	}
	if err := c.Init(tlsConfig); err != nil {
		return nil, errors.Wrap(err, "cannot initialize rest controller")
	}
	return c, nil
}

type controller struct {
	server                 http.Server
	router                 *gin.Engine
	addr                   string
	subpath                string
	logger                 zLogger.ZLogger
	dbClient               mediaserverdbproto.DBControllerClient
	actionControllerClient mediaserveractionproto.ActionControllerClient
	actionParams           map[string][]string
	itemCache              gcache.Cache
}

func (ctrl *controller) getParams(mediaType string, action string) ([]string, error) {
	sig := fmt.Sprintf("%s::%s", mediaType, action)
	if params, ok := ctrl.actionParams[sig]; ok {
		return params, nil
	}
	resp, err := ctrl.actionControllerClient.GetParams(context.Background(), &mediaserveractionproto.ParamsParam{
		Type:   mediaType,
		Action: action,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "cannot get params for %s::%s", mediaType, action)
	}
	ctrl.actionParams[sig] = resp.GetValues()
	return resp.GetValues(), nil
}

func (ctrl *controller) Init(tlsConfig *tls.Config) error {
	ctrl.router.GET("/:collection/:signature/:action", ctrl.action)
	ctrl.router.GET("/:collection/:signature/:action/*params", ctrl.action)

	ctrl.server = http.Server{
		Addr:      ctrl.addr,
		Handler:   ctrl.router,
		TLSConfig: tlsConfig,
	}

	/*
		if err := http2.ConfigureServer(&ctrl.server, nil); err != nil {
			return errors.WithStack(err)
		}
	*/

	return nil
}

func (ctrl *controller) Start(wg *sync.WaitGroup) {
	go func() {
		wg.Add(1)
		defer wg.Done() // let main know we are done cleaning up

		if ctrl.server.TLSConfig == nil {
			fmt.Printf("starting server at http://%s\n", ctrl.addr)
			if err := ctrl.server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				// unexpected error. port in use?
				fmt.Errorf("server on '%s' ended: %v", ctrl.addr, err)
			}
		} else {
			fmt.Printf("starting server at https://%s\n", ctrl.addr)
			if err := ctrl.server.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
				// unexpected error. port in use?
				fmt.Errorf("server on '%s' ended: %v", ctrl.addr, err)
			}
		}
		// always returns error. ErrServerClosed on graceful close
	}()
}

func (ctrl *controller) Stop() {
	ctrl.server.Shutdown(context.Background())
}

func (ctrl *controller) GracefulStop() {
	ctrl.server.Shutdown(context.Background())
}

func (ctrl *controller) action(c *gin.Context) {
	collection := c.Param("collection")
	signature := c.Param("signature")
	action := c.Param("action")
	paramStr := c.Param("params")
	ctrl.logger.Debug().Msgf("collection: %s, signature: %s, action: %s, params: %s", collection, signature, action, params)
	params := strings.Split(paramStr, "/")
	c.JSON(200, gin.H{
		"collection": collection,
		"signature":  signature,
		"action":     action,
		"params":     params,
	})
}
