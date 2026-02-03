package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

type WsMessage struct {
	IPAddress string `json:"address"`
	Message   string `json:"message"`
	Time      string `json:"time"`
	Host      string `json:"host"`
}

var addrFlag = flag.String("h", "localhost:8443;localhost:8444", "enter addresses separated by \";\"")
var connNumber = flag.Int("c", 25, "number of connections per host")
var addrList []string

func main() {
	flag.Parse()
	addrList = strings.Split(*addrFlag, ";")

	ctx, cancel := context.WithCancel(context.Background())
	g, gCtx := errgroup.WithContext(ctx)

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}

	go func() {
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
		<-interrupt
		cancel()
	}()

	for _, addr := range addrList {
		u := url.URL{Scheme: "ws", Host: addr, Path: "/ws"}
		// u := url.URL{Scheme: "wss", Host: addr, Path: "/ws"}
		headers := http.Header{}
		headers.Set("Origin", "http://"+addr)
		// headers.Set("Origin", "https://"+addr)
		log.Infof("connecting to %s", u.String())

		for i := 0; i < *connNumber; i++ {
			conn, resp, err := dialer.Dial(u.String(), headers)
			if err != nil {
				if resp != nil {
					log.Errorf("response: %s", resp.Status)
				}
				log.Fatal("dial error:", err)
			}

			time.Sleep(time.Millisecond * 500)
			g.Go(func() error {
				//log.Println(i, u.String())
				defer conn.Close()
				return sendMessages(gCtx, conn)
			})
		}
	}

	if err := g.Wait(); err != nil {
		log.Info(err)
	}
}

func sendMessages(ctx context.Context, conn *websocket.Conn) error {
	ticker := time.NewTicker(time.Millisecond * 500)
	defer ticker.Stop()

	host, err := os.Hostname()
	if err != nil {
		return err
	}
	ip, err := hostIP()
	if err != nil {
		return err
	}
	log.Info("start sending, messages")

	for {
		select {
		case <-ctx.Done():
			err = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				return err
			}
			return errors.New("websocket client stopped")
		case t := <-ticker.C:
			msg := WsMessage{
				IPAddress: ip,
				Message:   t.String() + "Hello",
				Time:      time.Now().Format("15:04"),
				Host:      host,
			}
			msgByte, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			err = conn.WriteMessage(websocket.TextMessage, msgByte)
			if err != nil {
				return err
			}
		}
	}
}

func hostIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	log.Info(conn.LocalAddr())
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
