package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
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

type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"username"`
	Password string `json:"password"`
}

var addrFlag = flag.String("h", "localhost:8443;localhost:8444", "enter addresses separated by \";\"")
var connNumber = flag.Int("c", 25, "number of connections per host")
var useTls = flag.Bool("tls", false, "use tls for connections")
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

	scheme := "ws"
	httpProtocol := "http"
	if *useTls {
		scheme = "wss"
		httpProtocol = "https"
	}

	for _, addr := range addrList {
		u := url.URL{Scheme: scheme, Host: addr, Path: "/ws"}
		baseURL := fmt.Sprintf("%s://%s", httpProtocol, addr)
		headers := http.Header{}
		log.Info(addr)
		headers.Set("Origin", baseURL)
		log.Infof("connecting to %s", u.String())

		cookie, _ := cookiejar.New(nil)
		client := &http.Client{
			Jar:     cookie,
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}

		for i := 0; i < *connNumber; i++ {
			username := fmt.Sprintf("%s_%d", "testuser", i)
			email := fmt.Sprintf("%s_%d@mail.com", "testuser", i)
			password := fmt.Sprintf("testpass%d", i)

			err := registerUser(client, baseURL, username, email, password)
			if err != nil {
				log.Warnf("Registration error: %v", err)
			}

			err = loginUser(client, baseURL, email, password)
			if err != nil {
				log.Errorf("Login error: %v", err)
				continue
			}

			cookies := client.Jar.Cookies(&url.URL{Scheme: httpProtocol, Host: addr})
			var cookieValues []string
			for _, c := range cookies {
				cookieValues = append(cookieValues, c.String())
			}
			if len(cookieValues) > 0 {
				headers.Set("Cookie", strings.Join(cookieValues, "; "))
			}

			conn, resp, err := dialer.Dial(u.String(), headers)
			if err != nil {
				if resp != nil {
					log.Errorf("response: %s", resp.Status)
				}
				log.Fatal("dial error:", err)
			}

			conn.SetReadDeadline(time.Now().Add(9 * time.Second))
			conn.SetPingHandler(func(appData string) error {
				conn.SetReadDeadline(time.Now().Add(9 * time.Second)) // обновляем read deadline
				return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(1*time.Second))
			})

			wg.Add(2) // 1 for writer, 1 for reader
			go func(c *websocket.Conn, user string) {
				defer wg.Done()
				defer c.Close()
				if err := sendMessages(ctx, c, host, ip, user); err != nil {
					log.Errorf("Error sending msg: %v", err)
				}
			}(conn, username)

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

func sendMessages(ctx context.Context, conn *websocket.Conn, host, ip, user string) error {
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
				Message:   fmt.Sprintf("[%s] %s", user, t.String()),
				Time:      time.Now().Format("15:04:05"),
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

func registerUser(client *http.Client, baseURL, username, email, password string) error {
	req := RegisterRequest{
		Username: username,
		Email:    email,
		Password: password,
	}
	body, _ := json.Marshal(req)

	resp, err := client.Post(baseURL+"/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register failed: %s - %s", resp.Status, string(body))
	}
	return nil
}

func loginUser(client *http.Client, baseURL, email, password string) error {
	req := LoginRequest{
		Email:    email,
		Password: password,
	}
	body, _ := json.Marshal(req)

	resp, err := client.Post(baseURL+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("login failed: %s - %s", resp.Status, string(body))
	}
	return nil
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
