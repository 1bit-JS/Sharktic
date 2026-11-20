package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// serviceTimeout is the most a request may take before the app gives up
	// on the connection, so that a stuck service never holds the sampler.
	serviceTimeout = 2 * time.Second
	// serviceRetry is how long the app waits to connect again.
	serviceRetry = 10 * time.Second
)

// serviceStatus tells whether the service is installed, and the process it
// runs as, 0 if stopped. The service manager tells it to any user.
func serviceStatus() (installed bool, pid uint32) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, 0
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(serviceName)
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false, 0
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS_PROCESS
	var n uint32
	if windows.QueryServiceStatusEx(s, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &n) != nil {
		return true, 0
	}
	if st.CurrentState != windows.SERVICE_RUNNING {
		return true, 0
	}
	return true, st.ProcessId
}

// serviceConn is the app's connection to the service.
type serviceConn struct {
	f   *os.File
	in  *bufio.Reader
	out *json.Encoder
}

// dialService connects to the service, and checks that the pipe is the
// service's: anyone may make a pipe of the same name while it is stopped.
func dialService() (*serviceConn, error) {
	_, pid := serviceStatus()
	if pid == 0 {
		return nil, errors.New("the service is not running")
	}
	name, _ := windows.UTF16PtrFromString(servicePipe)
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return nil, err
	}
	var server uint32
	if err := windows.GetNamedPipeServerProcessId(h, &server); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if server != pid {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("the pipe is process %d's, not the service's (%d)", server, pid)
	}
	f := os.NewFile(uintptr(h), servicePipe)
	return &serviceConn{f: f, in: bufio.NewReaderSize(f, maxRequest), out: json.NewEncoder(f)}, nil
}

// ask sends a request and reads the reply, within serviceTimeout. On a
// timeout it cancels the pending read; the connection is then of no more use.
func (c *serviceConn) ask(op string) (serviceReply, error) {
	type result struct {
		r   serviceReply
		err error
	}
	done := make(chan result, 1)
	go func() {
		var r serviceReply
		err := c.out.Encode(serviceRequest{Op: op})
		if err == nil {
			var line []byte
			if line, err = c.in.ReadBytes('\n'); err == nil {
				err = json.Unmarshal(line, &r)
			}
		}
		done <- result{r, err}
	}()
	select {
	case res := <-done:
		if res.err == nil && res.r.Error != "" {
			res.err = errors.New(res.r.Error)
		}
		return res.r, res.err
	case <-time.After(serviceTimeout):
		windows.CancelIoEx(windows.Handle(c.f.Fd()), nil)
		return serviceReply{}, errors.New("the service did not answer")
	}
}

func (c *serviceConn) close() { c.f.Close() }

// serviceSensor reads what the service gives into a Snapshot, on the
// sampler's goroutine. It connects when the service is there, and again after
// it has gone.
type serviceSensor struct {
	conn    *serviceConn
	retryAt time.Time
	// protocol is that of the connected service; an older one is told to be
	// updated.
	protocol int
	version  string
}

func (s *serviceSensor) read(out *Snapshot) {
	now := time.Now()
	if s.conn == nil {
		if now.Before(s.retryAt) && !serviceRetryNow.Swap(false) {
			return
		}
		s.retryAt = now.Add(serviceRetry)
		conn, err := dialService()
		if err != nil {
			return
		}
		hello, err := conn.ask("hello")
		if err != nil {
			conn.close()
			return
		}
		s.conn, s.protocol, s.version = conn, hello.Protocol, hello.Version
	}
	r, err := s.conn.ask("read")
	if err != nil {
		s.conn.close()
		s.conn = nil
		return
	}
	out.ServiceRunning, out.ServiceVersion = true, s.version
	if r.HasCPUTemp {
		out.CPUTemp, out.HasCPUTemp = r.CPUTemp, true
	}
}
