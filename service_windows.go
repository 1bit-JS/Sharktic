package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// The service does what needs administrator rights, so that the app itself
// never runs with them. It runs a copy of Sharktic.exe from Program Files, as
// LocalSystem, and answers the app over a named pipe. It takes only the
// requests below, and so far they all only read.

const (
	serviceName        = "SharkticService"
	serviceDisplayName = "Sharktic Service"
	servicePipe        = `\\.\pipe\SharkticService`
	// serviceProtocol changes when a request or a reply does, so that the app
	// can tell an older service to be updated.
	serviceProtocol = 2

	// servicePipeSDDL lets SYSTEM and administrators in fully, and the user
	// signed in at the console read and write; no one over the network.
	servicePipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"
	pipeInstances   = 4
	maxRequest      = 4096
)

type serviceRequest struct {
	Op string `json:"op"` // "hello" or "read"
}

type serviceReply struct {
	Protocol   int     `json:"protocol"`
	Version    string  `json:"version,omitempty"`
	CPUTemp    float64 `json:"cpuTemp,omitempty"`
	HasCPUTemp bool    `json:"hasCPUTemp,omitempty"`
	// Fans are the board's, from its Super I/O chip.
	Fans []FanSpeed `json:"fans,omitempty"`
	// SuperIO names the chip found, or tells why there is none.
	SuperIO string `json:"superIO,omitempty"`
	// CPUSource tells where the CPU temperature comes from, or why there is none.
	CPUSource string `json:"cpuSource,omitempty"`
	Error     string `json:"error,omitempty"`
}

// serviceState is the hardware the service has opened, which it opens again
// later if it could not.
type serviceState struct {
	version string

	mu        sync.Mutex
	cpu       *cpuThermal
	cpuErr    error
	cpuRetry  time.Time
	sio       *superIO
	sioNote   string
	sioRetry  time.Time
	sioDone   bool // looked for, and none to look for again
	lastRead  time.Time
	lastReply serviceReply
}

const (
	serviceRetryEvery = 30 * time.Second
	// serviceReadEvery is the least time between two reads of the hardware,
	// however many requests come.
	serviceReadEvery = 500 * time.Millisecond
)

func (s *serviceState) handle(req serviceRequest) serviceReply {
	switch req.Op {
	case "hello":
		return serviceReply{Protocol: serviceProtocol, Version: s.version}
	case "read":
		return s.read()
	}
	return serviceReply{Protocol: serviceProtocol, Error: "unknown request"}
}

func (s *serviceState) read() serviceReply {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.lastRead) < serviceReadEvery {
		return s.lastReply
	}
	s.lastRead = now
	if s.cpu == nil && !now.Before(s.cpuRetry) {
		s.cpuRetry = now.Add(serviceRetryEvery)
		s.cpu, s.cpuErr = openCPUThermal()
		if s.cpuErr != nil {
			log.Println("cpu thermal:", s.cpuErr)
		} else {
			log.Println("cpu thermal:", s.cpu.source)
		}
	}
	if s.sio == nil && !s.sioDone && !now.Before(s.sioRetry) {
		s.sioRetry = now.Add(serviceRetryEvery)
		sio, note, err := openSuperIO()
		switch {
		case err != nil:
			log.Println("super i/o:", err)
			s.sioNote = "오류: " + err.Error()
		case sio == nil:
			s.sioNote, s.sioDone = note, true // looking again would not find one
		default:
			s.sio, s.sioNote = sio, note
		}
		log.Println("super i/o:", s.sioNote)
	}
	r := serviceReply{Protocol: serviceProtocol, SuperIO: s.sioNote}
	switch {
	case s.cpu != nil:
		r.CPUSource = s.cpu.source
	case s.cpuErr != nil:
		r.CPUSource = "오류: " + s.cpuErr.Error()
	}
	if s.cpu != nil {
		r.CPUTemp, r.HasCPUTemp = s.cpu.temperature()
	}
	if s.sio != nil {
		fans, err := s.sio.fans()
		if err != nil {
			log.Println("super i/o fans:", err)
		}
		r.Fans = fans
	}
	s.lastReply = r
	return r
}

func (s *serviceState) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cpu != nil {
		s.cpu.close()
		s.cpu = nil
	}
	if s.sio != nil {
		s.sio.close()
		s.sio = nil
	}
}

// servePipe answers on the pipe until the process ends.
func servePipe(state *serviceState) error {
	sd, err := windows.SecurityDescriptorFromString(servicePipeSDDL)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, _ := windows.UTF16PtrFromString(servicePipe)
	for first := true; ; first = false {
		mode := uint32(windows.PIPE_ACCESS_DUPLEX)
		if first {
			// Fails if another process made the pipe first, to pass for us.
			mode |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
		}
		h, err := windows.CreateNamedPipe(name, mode,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
			pipeInstances, maxRequest, maxRequest, 0, sa)
		if err != nil {
			if first {
				return err
			}
			// All instances are busy: wait for one.
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err := windows.ConnectNamedPipe(h, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			windows.CloseHandle(h)
			continue
		}
		go serveClient(state, h)
	}
}

// serveClient answers one request a line until the client goes.
func serveClient(state *serviceState, h windows.Handle) {
	f := os.NewFile(uintptr(h), servicePipe)
	defer f.Close()
	in := bufio.NewScanner(f)
	in.Buffer(make([]byte, maxRequest), maxRequest)
	out := json.NewEncoder(f)
	for in.Scan() {
		var req serviceRequest
		reply := serviceReply{Protocol: serviceProtocol, Error: "bad request"}
		if json.Unmarshal(in.Bytes(), &req) == nil {
			reply = state.handle(req)
		}
		if out.Encode(reply) != nil {
			return
		}
	}
}

// sharkticService is the service as the service manager runs it.
type sharkticService struct{ state *serviceState }

func (s *sharkticService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	failed := make(chan error, 1)
	go func() { failed <- servePipe(s.state) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-failed:
			log.Println("pipe:", err)
			s.state.close()
			return true, 1
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				s.state.close()
				return false, 0
			}
		}
	}
}

// maxServiceLog is the size at which the log starts over, keeping the last
// one as service.log.old.
const maxServiceLog = 1 << 20

// serviceLogPath is in the service's folder, which only administrators can
// change, so that no one can put a link there for the service to write
// through. Users can read it, for a diagnosis.
func serviceLogPath() (string, error) {
	dir, err := serviceDir()
	return filepath.Join(dir, "service.log"), err
}

func openServiceLog() (*os.File, error) {
	path, err := serviceLogPath()
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxServiceLog {
		os.Rename(path, path+".old")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// runService runs as the service, when Sharktic.exe is started with
// --service by the service manager.
func runService(version string) {
	if inService, err := svc.IsWindowsService(); err != nil || !inService {
		os.Exit(2) // only the service manager starts it
	}
	if f, err := openServiceLog(); err == nil {
		defer f.Close()
		log.SetOutput(f)
	}
	log.Printf("Sharktic service %s, protocol %d, started", version, serviceProtocol)
	defer log.Println("stopped")
	if err := svc.Run(serviceName, &sharkticService{state: &serviceState{version: version}}); err != nil {
		log.Println("service:", err)
		os.Exit(1)
	}
}
