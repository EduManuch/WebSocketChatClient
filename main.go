package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
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
	var wg sync.WaitGroup

	ip := hostIP()
	host, err := os.Hostname()
	if err != nil {
		log.Error(err)
	}

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

			conn.SetReadDeadline(time.Now().Add(9 * time.Second))
			// conn.SetPongHandler(func(string) error {
			// 	conn.SetReadDeadline(time.Now().Add(9 * time.Second))
			// 	return nil
			// })
			conn.SetPingHandler(func(appData string) error {
				conn.SetReadDeadline(time.Now().Add(9 * time.Second)) // обновляем read deadline
				return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(1*time.Second))
			})

			wg.Add(2) // 1 for writer, 1 for reader
			go func(c *websocket.Conn) {
				defer wg.Done()
				defer c.Close()
				if err := sendMessages(ctx, c, host, ip); err != nil {
					log.Errorf("Error sending msg: %v", err)
				}
			}(conn)

			go func(c *websocket.Conn) {
				defer wg.Done()
				defer c.Close()
				for {
					if _, _, err := c.ReadMessage(); err != nil {
						return
					}
				}
			}(conn)
		}
	}

	log.Infof("Started %v connections", *connNumber*len(addrList))
	<-ctx.Done()
	wg.Wait()
	log.Info("App stopped")
}

func sendMessages(ctx context.Context, conn *websocket.Conn, host, ip string) error {
	ticker := time.NewTicker(time.Millisecond * 500)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				return err
			}
			return nil
		case t := <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
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

func hostIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Error(err)
		return ""
	}
	defer conn.Close()
	log.Info(conn.LocalAddr())
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
