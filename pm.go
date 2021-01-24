package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"github.com/uscott/go-tools/errs"
)

const DefaultAddress string = ":8080"

type ProcessManager struct {
	Address     string
	NetListener *net.Listener
	Server      *http.Server
	Signal      chan os.Signal
	Sub         Subordinate
}

type Subordinate interface {
	Done() chan struct{}
	Error() chan error
	Run()
	WaitTime() time.Duration
}

type Listener struct {
	Address  string `json:"address"`
	FD       int    `json:"fd"`
	Filename string `json:"filename"`
}

func NewProcessManager(address string) (*ProcessManager, error) {
	if address == "" {
		address = DefaultAddress
	}
	var err error
	pm := &ProcessManager{Address: address}
	pm.NetListener = new(net.Listener)
	pm.Server = new(http.Server)
	pm.Signal = make(chan os.Signal, 1024)
	if err = pm.CreateOrImportListener(); err != nil {
		return nil, err
	}
	if err = pm.StartServer(); err != nil {
		return nil, err
	}
	return pm, nil
}

func (p *ProcessManager) ImportListener() (*net.Listener, error) {
	// Extract the encoded listener metadata from the environment.
	listenerEnv := os.Getenv("LISTENER")
	if listenerEnv == "" {
		return nil, fmt.Errorf("unable to find LISTENER environment variable")
	}
	// Unmarshal the listener metadata.
	l := Listener{}
	err := json.Unmarshal([]byte(listenerEnv), &l)
	if err != nil {
		return nil, err
	}
	if p.Address == "" {
		p.Address = DefaultAddress
	}
	address := p.Address
	if l.Address != address {
		return nil, fmt.Errorf("unable to find listener for %v", address)
	}
	// The file has already been passed to this process, extract the file
	// descriptor and name from the metadata to rebuild/find the *os.File for
	// the listener.
	listenerFile := os.NewFile(uintptr(l.FD), l.Filename)
	if listenerFile == nil {
		return nil, fmt.Errorf("unable to create listener file: %v", err.Error())
	}
	defer listenerFile.Close()
	// Create a net.Listener from the *os.File.
	ln, err := net.FileListener(listenerFile)
	if err != nil {
		return nil, err
	}
	return &ln, nil
}

func (p *ProcessManager) CreateListener() (*net.Listener, error) {
	if p.Address == "" {
		p.Address = DefaultAddress
	}
	address := p.Address
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return &ln, nil
}

func (p *ProcessManager) CreateOrImportListener() error {
	// Try and import a listener for addr. If it's found, use it.
	p.NetListener = nil
	ln, err := p.ImportListener()
	if err == nil {
		p.NetListener = ln
		return nil
	}
	// No listener was imported, that means this process has to create one.
	ln, err = p.CreateListener()
	if err != nil {
		return err
	}
	p.NetListener = ln
	return nil
}

func (p *ProcessManager) GetListenerFile() (*os.File, error) {
	ln := p.NetListener
	if ln == nil {
		return nil, errors.Wrap(errs.NilPtrUnexpected, " - NetListener")
	}
	switch t := (*ln).(type) {
	case *net.TCPListener:
		return t.File()
	case *net.UnixListener:
		return t.File()
	}
	return nil, fmt.Errorf("unsupported listener: %T", *ln)
}

func (p *ProcessManager) ForkChild() (*os.Process, error) {
	// Get the file descriptor for the listener and marshal the metadata to pass
	// to the child in the environment.
	lnFile, err := p.GetListenerFile()
	if err != nil {
		return nil, err
	}
	defer lnFile.Close()
	if p.Address == "" {
		p.Address = DefaultAddress
	}
	address := p.Address
	l := Listener{
		Address:  address,
		FD:       3,
		Filename: lnFile.Name(),
	}
	listenerEnv, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	// Pass stdin, stdout, and stderr along with the listener to the child.
	files := []*os.File{
		os.Stdin,
		os.Stdout,
		os.Stderr,
		lnFile,
	}
	// Get current environment and add in the listener to it.
	environment := append(os.Environ(), "LISTENER="+string(listenerEnv))
	// Get current process name and directory.
	execName, err := os.Executable()
	if err != nil {
		return nil, err
	}
	execDir := filepath.Dir(execName)
	// Spawn child process.
	proc, err := os.StartProcess(execName, os.Args, &os.ProcAttr{
		Dir:   execDir,
		Env:   environment,
		Files: files,
		Sys:   &syscall.SysProcAttr{},
	})
	if err != nil {
		return nil, err
	}
	return proc, nil
}

func (p *ProcessManager) Run() error {

	go p.Sub.Run()

	signal.Notify(p.Signal, syscall.SIGHUP, syscall.SIGUSR2, syscall.SIGINT, syscall.SIGQUIT)

	for {

		select {

		case s := <-p.Signal:

			switch s {
			case syscall.SIGHUP:
				// Fork a child process.
				w := p.Sub.WaitTime()
				time.Sleep(w)
				_, err := p.ForkChild()
				if err != nil {
					continue
				}
				// Create a context that will expire in 5 seconds and use this as a
				// timeout to Shutdown.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				p.Sub = nil
				// Return any errors during shutdown.
				return p.Server.Shutdown(ctx)
			case syscall.SIGUSR2:
				// Fork a child process.
				w := p.Sub.WaitTime()
				time.Sleep(w)
				_, err := p.ForkChild()
				if err != nil {
					continue
				}
			case syscall.SIGINT, syscall.SIGQUIT:
				// Create a context that will expire in 5 seconds and use this as a
				// timeout to Shutdown.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// Return any errors during shutdown.
				return p.Server.Shutdown(ctx)
			}

		case e := <-p.Sub.Error():
			switch e {
			case nil:
			default:
				// Fork a child process.
				w := p.Sub.WaitTime()
				time.Sleep(w)
				_, err := p.ForkChild()
				if err != nil {
					continue
				}
				// Create a context that will expire in 5 seconds and use this as a
				// timeout to Shutdown.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// Return any errors during shutdown.
				return p.Server.Shutdown(ctx)
			}

		case <-p.Sub.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Return any errors during shutdown.
			return p.Server.Shutdown(ctx)

		default:
			time.Sleep(100 * time.Millisecond)

		}
	}
}

func (p *ProcessManager) Handler(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, "Hello from %v!\n", os.Getpid())
}

func (p *ProcessManager) StartServer() (err error) {

	http.HandleFunc("/hello", p.Handler)

	if p.Address == "" {
		p.Address = DefaultAddress
	}

	p.Server = &http.Server{
		Addr: p.Address,
	}

	go func() {
		err = p.Server.Serve(*p.NetListener)
	}()

	return
}
