package forward

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhaolanbao/oxy/testutils"
	"golang.org/x/net/websocket"
)

func TestWebSocketTCPClose(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	errChan := make(chan error, 1)
	upgrader := gorillawebsocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				errChan <- err
				break
			}
		}
	}))
	defer srv.Close()

	proxy := createProxyWithForwarder(f, srv.URL)

	proxyAddr := proxy.Listener.Addr().String()
	_, conn, err := newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
	).open()
	require.NoError(t, err)
	conn.Close()

	serverErr := <-errChan

	wsErr, ok := serverErr.(*gorillawebsocket.CloseError)
	assert.Equal(t, true, ok)
	assert.Equal(t, 1006, wsErr.Code)
}

func TestWebsocketConnectionClosedHook(t *testing.T) {
	closed := make(chan struct{})
	f, err := New(WebsocketConnectionClosedHook(func(req *http.Request, conn net.Conn) {
		close(closed)
	}))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		msg := make([]byte, 4)
		conn.Read(msg)
		conn.Write(msg)
		conn.Close()
	}))

	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	serverAddr := proxy.Listener.Addr().String()

	headers := http.Header{}
	webSocketURL := "ws://" + serverAddr + "/ws"
	headers.Add("Origin", webSocketURL)

	conn, resp, err := gorillawebsocket.DefaultDialer.Dial(webSocketURL, headers)
	require.NoError(t, err, "Error during Dial with response: %+v", resp)

	conn.WriteMessage(gorillawebsocket.TextMessage, []byte("OK"))
	fmt.Println(conn.ReadMessage())

	conn.Close()

	select {
	case <-time.After(time.Second):
		t.Errorf("Websocket Hook not called")
	case <-closed:

	}
}

func TestWebSocketPingPong(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	var upgrader = gorillawebsocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		CheckOrigin: func(*http.Request) bool {
			return true
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(writer http.ResponseWriter, request *http.Request) {
		ws, err := upgrader.Upgrade(writer, request, nil)
		require.NoError(t, err)

		ws.SetPingHandler(func(appData string) error {
			ws.WriteMessage(gorillawebsocket.PongMessage, []byte(appData+"Pong"))
			return nil
		})

		ws.ReadMessage()
	})

	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	serverAddr := proxy.Listener.Addr().String()

	headers := http.Header{}
	webSocketURL := "ws://" + serverAddr + "/ws"
	headers.Add("Origin", webSocketURL)

	conn, resp, err := gorillawebsocket.DefaultDialer.Dial(webSocketURL, headers)
	require.NoError(t, err, "Error during Dial with response: %+v", resp)
	defer conn.Close()

	goodErr := fmt.Errorf("signal: %s", "Good data")
	badErr := fmt.Errorf("signal: %s", "Bad data")
	conn.SetPongHandler(func(data string) error {
		if data == "PingPong" {
			return goodErr
		}
		return badErr
	})

	conn.WriteControl(gorillawebsocket.PingMessage, []byte("Ping"), time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()

	if err != goodErr {
		require.NoError(t, err)
	}
}

func TestWebSocketEcho(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		msg := make([]byte, 4)
		conn.Read(msg)
		fmt.Println(string(msg))
		conn.Write(msg)
		conn.Close()
	}))

	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	serverAddr := proxy.Listener.Addr().String()

	headers := http.Header{}
	webSocketURL := "ws://" + serverAddr + "/ws"
	headers.Add("Origin", webSocketURL)

	conn, resp, err := gorillawebsocket.DefaultDialer.Dial(webSocketURL, headers)
	require.NoError(t, err, "Error during Dial with response: %+v", resp)

	conn.WriteMessage(gorillawebsocket.TextMessage, []byte("OK"))
	fmt.Println(conn.ReadMessage())

	conn.Close()
}

func TestWebSocketNumGoRoutine(t *testing.T) {
	t.Skip("Flaky on goroutine")
	f, err := New()
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		msg := make([]byte, 4)
		conn.Read(msg)
		fmt.Println(string(msg))
		conn.Write(msg)
		conn.Close()
	}))

	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	serverAddr := proxy.Listener.Addr().String()

	num := runtime.NumGoroutine()

	headers := http.Header{}
	webSocketURL := "ws://" + serverAddr + "/ws"
	headers.Add("Origin", webSocketURL)

	conn, resp, err := gorillawebsocket.DefaultDialer.Dial(webSocketURL, headers)
	require.NoError(t, err, "Error during Dial with response: %+v", resp)

	conn.WriteMessage(gorillawebsocket.TextMessage, []byte("OK"))
	fmt.Println(conn.ReadMessage())

	conn.Close()

	time.Sleep(time.Second)
	assert.Equal(t, num, runtime.NumGoroutine())
}

func TestWebSocketServerWithoutCheckOrigin(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		return true
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, message, err := c.ReadMessage()
			if err != nil {
				break
			}
			err = c.WriteMessage(mt, message)
			if err != nil {
				break
			}
		}
	}))
	defer srv.Close()

	proxy := createProxyWithForwarder(f, srv.URL)
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()
	resp, err := newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
		withData("ok"),
		withOrigin("http://127.0.0.2"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func TestWebSocketRequestWithOrigin(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	upgrader := gorillawebsocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, message, err := c.ReadMessage()
			if err != nil {
				break
			}
			err = c.WriteMessage(mt, message)
			if err != nil {
				break
			}
		}
	}))
	defer srv.Close()

	proxy := createProxyWithForwarder(f, srv.URL)
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()
	_, err = newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
		withData("echo"),
		withOrigin("http://127.0.0.2"),
	).send()
	require.EqualError(t, err, "bad status")

	resp, err := newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
		withData("ok"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func TestWebSocketRequestWithQueryParams(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	upgrader := gorillawebsocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		assert.Equal(t, "test", r.URL.Query().Get("query"))
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			err = conn.WriteMessage(mt, message)
			if err != nil {
				break
			}
		}
	}))
	defer srv.Close()

	proxy := createProxyWithForwarder(f, srv.URL)
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()

	resp, err := newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws?query=test"),
		withData("ok"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func TestWebSocketRequestWithHeadersInResponseWriter(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		conn.Close()
	}))
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		w.Header().Set("HEADER-KEY", "HEADER-VALUE")
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	serverAddr := proxy.Listener.Addr().String()

	headers := http.Header{}
	webSocketURL := "ws://" + serverAddr + "/ws"
	headers.Add("Origin", webSocketURL)
	conn, resp, err := gorillawebsocket.DefaultDialer.Dial(webSocketURL, headers)
	require.NoError(t, err, "Error during Dial with response: %+v", err, resp)
	defer conn.Close()

	assert.Equal(t, "HEADER-VALUE", resp.Header.Get("HEADER-KEY"))
}

func TestWebSocketRequestWithEncodedChar(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	upgrader := gorillawebsocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		assert.Equal(t, "/%3A%2F%2F", r.URL.EscapedPath())
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			err = conn.WriteMessage(mt, message)
			if err != nil {
				break
			}
		}
	}))
	defer srv.Close()

	proxy := createProxyWithForwarder(f, srv.URL)
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()

	resp, err := newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/%3A%2F%2F"),
		withData("ok"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func TestDetectsWebSocketRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		conn.Write([]byte("ok"))
		conn.Close()
	}))
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		websocketRequest := IsWebsocketRequest(req)
		assert.Equal(t, true, websocketRequest)
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	serverAddr := srv.Listener.Addr().String()

	resp, err := newWebsocketRequest(
		withServer(serverAddr),
		withPath("/ws"),
		withData("echo"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func TestWebSocketUpgradeFailed(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(400)
	})
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path // keep the original path

		if path == "/ws" {
			// Set new backend URL
			req.URL = testutils.ParseURI(srv.URL)
			req.URL.Path = path
			websocketRequest := IsWebsocketRequest(req)
			assert.Equal(t, true, websocketRequest)
			f.ServeHTTP(w, req)
		} else {
			w.WriteHeader(200)
		}
	})
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()
	conn, err := net.DialTimeout("tcp", proxyAddr, dialTimeout)

	require.NoError(t, err)
	defer conn.Close()

	req, err := http.NewRequest(http.MethodGet, "ws://127.0.0.1/ws", nil)
	require.NoError(t, err)

	req.Header.Add("upgrade", "websocket")
	req.Header.Add("Connection", "upgrade")

	req.Write(conn)

	// First request works with 400
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	require.NoError(t, err)

	assert.Equal(t, 400, resp.StatusCode)
}

func TestForwardsWebsocketTraffic(t *testing.T) {
	f, err := New()
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		conn.Write([]byte("ok"))
		conn.Close()
	}))
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := createProxyWithForwarder(f, srv.URL)
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()
	resp, err := newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
		withData("echo"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func createTLSWebsocketServer() *httptest.Server {
	upgrader := gorillawebsocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			err = conn.WriteMessage(mt, message)
			if err != nil {
				break
			}
		}
	}))
	return srv
}

func createProxyWithForwarder(forwarder *Forwarder, URL string) *httptest.Server {
	return testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path // keep the original path
		// Set new backend URL
		req.URL = testutils.ParseURI(URL)
		req.URL.Path = path

		forwarder.ServeHTTP(w, req)
	})
}

func TestWebSocketTransferTLSConfig(t *testing.T) {
	srv := createTLSWebsocketServer()
	defer srv.Close()

	forwarderWithoutTLSConfig, err := New()
	require.NoError(t, err)

	proxyWithoutTLSConfig := createProxyWithForwarder(forwarderWithoutTLSConfig, srv.URL)
	defer proxyWithoutTLSConfig.Close()

	proxyAddr := proxyWithoutTLSConfig.Listener.Addr().String()

	_, err = newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
		withData("ok"),
	).send()

	require.EqualError(t, err, "bad status")

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	forwarderWithTLSConfig, err := New(RoundTripper(transport))
	require.NoError(t, err)

	proxyWithTLSConfig := createProxyWithForwarder(forwarderWithTLSConfig, srv.URL)
	defer proxyWithTLSConfig.Close()

	proxyAddr = proxyWithTLSConfig.Listener.Addr().String()

	resp, err := newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
		withData("ok"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)

	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	forwarderWithTLSConfigFromDefaultTransport, err := New()
	require.NoError(t, err)

	proxyWithTLSConfigFromDefaultTransport := createProxyWithForwarder(forwarderWithTLSConfigFromDefaultTransport, srv.URL)
	defer proxyWithTLSConfig.Close()

	proxyAddr = proxyWithTLSConfigFromDefaultTransport.Listener.Addr().String()

	resp, err = newWebsocketRequest(
		withServer(proxyAddr),
		withPath("/ws"),
		withData("ok"),
	).send()

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

const dialTimeout = time.Second

type websocketRequestOpt func(w *websocketRequest)

func withServer(server string) websocketRequestOpt {
	return func(w *websocketRequest) {
		w.ServerAddr = server
	}
}

func withPath(path string) websocketRequestOpt {
	return func(w *websocketRequest) {
		w.Path = path
	}
}

func withData(data string) websocketRequestOpt {
	return func(w *websocketRequest) {
		w.Data = data
	}
}

func withOrigin(origin string) websocketRequestOpt {
	return func(w *websocketRequest) {
		w.Origin = origin
	}
}

func newWebsocketRequest(opts ...websocketRequestOpt) *websocketRequest {
	wsrequest := &websocketRequest{}
	for _, opt := range opts {
		opt(wsrequest)
	}
	if wsrequest.Origin == "" {
		wsrequest.Origin = "http://" + wsrequest.ServerAddr
	}
	if wsrequest.Config == nil {
		wsrequest.Config, _ = websocket.NewConfig(fmt.Sprintf("ws://%s%s", wsrequest.ServerAddr, wsrequest.Path), wsrequest.Origin)
	}
	return wsrequest
}

type websocketRequest struct {
	ServerAddr string
	Path       string
	Data       string
	Origin     string
	Config     *websocket.Config
}

func (w *websocketRequest) send() (string, error) {
	conn, _, err := w.open()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(w.Data)); err != nil {
		return "", err
	}
	var msg = make([]byte, 512)
	var n int
	n, err = conn.Read(msg)
	if err != nil {
		return "", err
	}

	received := string(msg[:n])
	return received, nil
}

func (w *websocketRequest) open() (*websocket.Conn, net.Conn, error) {
	client, err := net.DialTimeout("tcp", w.ServerAddr, dialTimeout)
	if err != nil {
		return nil, nil, err
	}
	conn, err := websocket.NewClient(w.Config, client)
	if err != nil {
		return nil, nil, err
	}
	return conn, client, err
}
