/*
@Time : 2020/11/23 14:50
@Author : Tux
@Description :
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"
)

func main() {
	var parameters WHSvrParameters

	// 获取命令行参数
	flag.IntVar(&parameters.port, "port", 443, "Webhook server port.")
	flag.StringVar(&parameters.certFile, "tlsCertFile", "/etc/webhook/certs/cert.pem", "File containing the x509 Certificate for HTTPS.")
	flag.StringVar(&parameters.keyFile, "tlsKeyFile", "/etc/webhook/certs/key.pem", "File containing the x509 private key to --tlsCertFile.")
	flag.Parse()

	// 加载 TLS 配置证书
	pair, err := tls.LoadX509KeyPair(parameters.certFile, parameters.keyFile)
	if err != nil {
		klog.Errorf("Failed to load key pair: %v", err)
	}

	// 实例化 webhookServer
	whsvr := WebhookServer{
		server: &http.Server{
			Addr:      fmt.Sprintf(":%v", parameters.port),
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}},
		},
	}

	// define http server and server handler
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", whsvr.serve)
	mux.HandleFunc("/validate", whsvr.serve)
	whsvr.server.Handler = mux

	// 监听 http
	go func() {
		if err := whsvr.server.ListenAndServeTLS("", ""); err != nil {
			klog.Errorf("Failed to listen serve webhook server: %v", err)
		}
	}()

	klog.Info("Server started")

	// listening OS shutdown signal
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	klog.Infof("Got OS shutdown signal, shutting down webhook server gracefully...")
	whsvr.server.Shutdown(context.Background())
}
