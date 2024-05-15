package web

import (
	"context"
	"crypto/tls"
	"emperror.dev/errors"
	"fmt"
	"github.com/gin-gonic/gin"
	mediaserverdbproto "github.com/je4/mediaserverproto/v2/pkg/mediaserverdb/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

func NewController(addr, extAddr string, tlsConfig *tls.Config, dbClient mediaserverdbproto.DBControllerClient, logger zLogger.ZLogger) (*controller, error) {
	u, err := url.Parse(extAddr)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid external address '%s'", extAddr)
	}
	subpath := "/" + strings.Trim(u.Path, "/")

	router := gin.Default()

	c := &controller{
		addr:     addr,
		router:   router,
		subpath:  subpath,
		logger:   logger,
		dbClient: dbClient,
	}
	if err := c.Init(tlsConfig); err != nil {
		return nil, errors.Wrap(err, "cannot initialize rest controller")
	}
	return c, nil
}

type controller struct {
	server   http.Server
	router   *gin.Engine
	addr     string
	subpath  string
	logger   zLogger.ZLogger
	dbClient mediaserverdbproto.DBControllerClient
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
	params := c.Param("params")
	ctrl.logger.Debug().Msgf("collection: %s, signature: %s, action: %s, params: %s", collection, signature, action, params)
	c.JSON(200, gin.H{
		"collection": collection,
		"signature":  signature,
		"action":     action,
		"params":     params,
	})
}
