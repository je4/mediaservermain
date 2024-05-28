package web

import (
	"context"
	"crypto/tls"
	"emperror.dev/errors"
	"fmt"
	"github.com/bluele/gcache"
	"github.com/gin-gonic/gin"
	"github.com/je4/mediaserveraction/v2/pkg/actionCache"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

type itemIdentifier struct {
	collection string
	signature  string
}

func NewMainController(addr, extAddr string, tlsConfig *tls.Config, dbClient mediaserverproto.DatabaseClient, actionControllerClient mediaserverproto.ActionClient, vfs fs.FS, itemCacheSize int, itemCacheTimout time.Duration, logger zLogger.ZLogger) (*mainController, error) {
	u, err := url.Parse(extAddr)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid external address '%s'", extAddr)
	}
	subpath := "/" + strings.Trim(u.Path, "/")

	gin.SetMode(gin.DebugMode)
	router := gin.Default()

	_logger := logger.With().Str("httpService", "mainController").Logger()
	c := &mainController{
		addr:                   addr,
		router:                 router,
		subpath:                subpath,
		logger:                 &_logger,
		dbClient:               dbClient,
		actionControllerClient: actionControllerClient,
		actionParams:           map[string][]string{},
		vfs:                    vfs,
		itemCache: gcache.New(itemCacheSize).
			LRU().Expiration(itemCacheTimout).
			LoaderFunc(func(key any) (any, error) {
				it, ok := key.(itemIdentifier)
				if !ok {
					return nil, errors.Errorf("invalid key type %T", key)
				}
				resp, err := dbClient.GetItem(context.Background(), &mediaserverproto.ItemIdentifier{
					Collection: it.collection,
					Signature:  it.signature,
				})
				if err != nil {
					return nil, errors.Wrapf(err, "cannot get item %s/%s", it.collection, it.signature)
				}
				return resp, nil
			}).
			Build(),
	}
	if err := c.Init(tlsConfig); err != nil {
		return nil, errors.Wrap(err, "cannot initialize rest controller")
	}
	return c, nil
}

type mainController struct {
	server                 http.Server
	router                 *gin.Engine
	addr                   string
	subpath                string
	logger                 zLogger.ZLogger
	dbClient               mediaserverproto.DatabaseClient
	actionControllerClient mediaserverproto.ActionClient
	actionParams           map[string][]string
	itemCache              gcache.Cache
	vfs                    fs.FS
}

func (ctrl *mainController) getParams(mediaType string, action string) ([]string, error) {
	sig := fmt.Sprintf("%s::%s", mediaType, action)
	if params, ok := ctrl.actionParams[sig]; ok {
		return params, nil
	}
	resp, err := ctrl.actionControllerClient.GetParams(context.Background(), &mediaserverproto.ParamsParam{
		Type:   mediaType,
		Action: action,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "cannot get params for %s::%s", mediaType, action)
	}
	ctrl.logger.Debug().Msgf("params for %s::%s: %v", mediaType, action, resp.GetValues())
	ctrl.actionParams[sig] = resp.GetValues()
	return resp.GetValues(), nil
}

func (ctrl *mainController) Init(tlsConfig *tls.Config) error {
	ctrl.router.GET("/:collection/:signature/:action", ctrl.action)
	ctrl.router.GET("/:collection/:signature/:action/*params", ctrl.action)

	ctrl.server = http.Server{
		Addr:      ctrl.addr,
		Handler:   ctrl.router,
		TLSConfig: tlsConfig,
	}

	return nil
}

func (ctrl *mainController) Start(wg *sync.WaitGroup) {
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

func (ctrl *mainController) Stop() {
	ctrl.server.Shutdown(context.Background())
}

func (ctrl *mainController) GracefulStop() {
	ctrl.server.Shutdown(context.Background())
}

var isUrlRegexp = regexp.MustCompile(`^[a-z]+://`)

func (ctrl *mainController) action(c *gin.Context) {
	collection := c.Param("collection")
	signature := c.Param("signature")
	action := c.Param("action")
	paramStr := c.Param("params")
	ctrl.logger.Debug().Msgf("collection: %s, signature: %s, action: %s, params: %s", collection, signature, action, paramStr)
	itemAny, err := ctrl.itemCache.Get(itemIdentifier{collection: collection, signature: signature})
	if err != nil {
		ctrl.logger.Error().Err(err).Msgf("cannot get item %s/%s", collection, signature)
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("cannot get item %s/%s: %v", collection, signature, err),
		})
		return
	}
	item, ok := itemAny.(*mediaserverproto.Item)
	if !ok {
		ctrl.logger.Error().Msgf("invalid item type %T", itemAny)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("invalid item type %T", itemAny),
		})
		return
	}

	if action == "metadata" {
		metadata, err := ctrl.dbClient.GetItemMetadata(context.Background(), &mediaserverproto.ItemIdentifier{
			Collection: collection,
			Signature:  signature,
		})
		if err != nil {
			stat, ok := status.FromError(err)
			if !ok || stat.Code() != codes.NotFound {
				ctrl.logger.Error().Err(err).Msgf("cannot get metadata for %s/%s", collection, signature)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": fmt.Sprintf("cannot get metadata for %s/%s: %v", collection, signature, err),
				})
				return
			}
			ctrl.logger.Error().Err(err).Msgf("%s/%s not found", collection, signature)
			c.JSON(http.StatusNotFound, gin.H{
				"error": fmt.Sprintf("%s/%s not found: %v", collection, signature, err),
			})
			return
		}

		c.Data(http.StatusOK, "application/json", []byte(metadata.GetValue()))
		return
	}

	var params = actionCache.ActionParams{}
	if !slices.Contains([]string{"item", "master"}, action) {
		allowedParams, err := ctrl.getParams(item.GetMetadata().GetType(), action)
		if err != nil {
			ctrl.logger.Error().Err(err).Msgf("cannot get params for %s::%s", item.GetMetadata().GetType(), action)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("cannot get params for %s::%s: %v", item.GetMetadata().GetType(), action, err),
			})
			return
		}
		params.SetString(paramStr, allowedParams)
	}

	cache, err := ctrl.dbClient.GetCache(context.Background(), &mediaserverproto.CacheRequest{
		Identifier: &mediaserverproto.ItemIdentifier{
			Collection: collection,
			Signature:  signature,
		},
		Action: action,
		Params: params.String(),
	})
	if err != nil {
		stat, ok := status.FromError(err)
		if !ok || stat.Code() != codes.NotFound {
			ctrl.logger.Error().Err(err).Msgf("cannot get cache for %s/%s/%s", collection, signature, action)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("cannot get cache for %s/%s/%s: %v", collection, signature, action, err),
			})
			return
		}
		coll, err := ctrl.dbClient.GetCollection(context.Background(), &mediaserverproto.CollectionIdentifier{
			Collection: collection,
		})
		if err != nil {
			ctrl.logger.Error().Err(err).Msgf("cannot get collection %s", collection)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("cannot get collection %s: %v", collection, err),
			})
			return
		}

		// cache not found, create it
		cache, err = ctrl.actionControllerClient.Action(context.Background(), &mediaserverproto.ActionParam{
			Item:    item,
			Action:  action,
			Params:  params,
			Storage: coll.GetStorage(),
		})
		if err != nil {
			ctrl.logger.Error().Err(err).Msgf("cannot get cache for %s/%s/%s: %v", collection, signature, action, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("cannot get cache for %s/%s/%s: %v", collection, signature, action, err),
			})
			return
		}
		if cache == nil {
			ctrl.logger.Error().Msgf("cannot get cache for %s/%s/%s: no cache", collection, signature, action)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("cannot get cache for %s/%s/%s: no cache", collection, signature, action),
			})
			return
		}
	}
	metadata := cache.GetMetadata()
	path := metadata.GetPath()
	if !isUrlRegexp.MatchString(path) {
		stor := metadata.GetStorage()
		if stor == nil {
			ctrl.logger.Error().Msgf("no storage defined for %s/%s/%s/%s", collection, signature, action, params.String())
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("no storage defined for %s/%s/%s/%s", collection, signature, action, params.String()),
			})
			return
		}
		path = stor.GetFilebase() + "/" + path
	}
	c.Header("Content-Type", metadata.GetMimeType())
	c.FileFromFS(path, http.FS(ctrl.vfs))
	return
}
