package test

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/api/alertmanager"
	"github.com/prometheus/common/model"
	"golang.org/x/net/context"
)

// AcceptanceTest provides declarative definition of given inputs and expected
// output of an Alertmanager setup.
type AcceptanceTest struct {
	*testing.T

	opts *AcceptanceOpts

	ams        []*Alertmanager
	collectors []*Collector

	actions map[float64][]func()
}

// AcceptanceOpts defines configuration paramters for an acceptance test.
type AcceptanceOpts struct {
	Tolerance time.Duration
	baseTime  time.Time
}

// expandTime returns the absolute time for the relative time
// calculated from the test's base time.
func (opts *AcceptanceOpts) expandTime(rel float64) time.Time {
	return opts.baseTime.Add(time.Duration(rel * float64(time.Second)))
}

// expandTime returns the relative time for the given time
// calculated from the test's base time.
func (opts *AcceptanceOpts) relativeTime(act time.Time) float64 {
	return float64(act.Sub(opts.baseTime)) / float64(time.Second)
}

// NewAcceptanceTest returns a new acceptance test with the base time
// set to the current time.
func NewAcceptanceTest(t *testing.T, opts *AcceptanceOpts) *AcceptanceTest {
	test := &AcceptanceTest{
		T:       t,
		opts:    opts,
		actions: map[float64][]func(){},
	}
	opts.baseTime = time.Now()

	return test
}

// freeAddress returns a new listen address not currently in use.
func freeAddress() string {
	// Let the OS allocate a free address, close it and hope
	// it is still free when starting Alertmanager.
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	defer l.Close()

	return l.Addr().String()
}

// Do sets the given function to be executed at the given time.
func (t *AcceptanceTest) Do(at float64, f func()) {
	t.actions[at] = append(t.actions[at], f)
}

// Alertmanager returns a new structure that allows starting an instance
// of Alertmanager on a random port.
func (t *AcceptanceTest) Alertmanager(conf string) *Alertmanager {
	am := &Alertmanager{
		t:    t,
		opts: t.opts,
	}

	cf, err := ioutil.TempFile("", "am_config")
	if err != nil {
		t.Fatal(err)
	}
	am.confFile = cf
	am.UpdateConfig(conf)

	am.addr = freeAddress()

	t.Logf("AM on %s", am.addr)

	client, err := alertmanager.New(alertmanager.Config{
		Address: fmt.Sprintf("http://%s", am.addr),
	})
	if err != nil {
		t.Error(err)
	}
	am.client = client

	am.cmd = exec.Command("../../alertmanager",
		"-config.file", cf.Name(),
		"-log.level", "debug",
		"-web.listen-address", am.addr,
	)

	var outb, errb bytes.Buffer
	am.cmd.Stdout = &outb
	am.cmd.Stderr = &errb

	t.ams = append(t.ams, am)

	return am
}

// Collector returns a new collector bound to the test instance.
func (t *AcceptanceTest) Collector(name string) *Collector {
	co := &Collector{
		t:         t.T,
		name:      name,
		opts:      t.opts,
		collected: map[float64][]model.Alerts{},
		expected:  map[Interval][]model.Alerts{},
	}
	t.collectors = append(t.collectors, co)

	return co
}

// Run starts all Alertmanagers and runs queries against them. It then checks
// whether all expected notifications have arrived at the expected destination.
func (t *AcceptanceTest) Run() {
	for _, am := range t.ams {
		am.Start()
		defer func(am *Alertmanager) {
			am.Terminate()
			am.cleanup()
		}(am)
	}

	t.runActions()

	var latest float64
	for _, coll := range t.collectors {
		if l := coll.latest(); l > latest {
			latest = l
		}
	}

	deadline := t.opts.expandTime(latest)
	time.Sleep(deadline.Sub(time.Now()))

	for _, coll := range t.collectors {
		report := coll.check()
		t.Log(report)
	}

	for _, am := range t.ams {
		t.Logf("stdout:\n%v", am.cmd.Stdout)
		t.Logf("stderr:\n%v", am.cmd.Stderr)
	}
}

// runActions performs the stored actions at the defined times.
func (t *AcceptanceTest) runActions() {
	var wg sync.WaitGroup

	for at, fs := range t.actions {
		ts := t.opts.expandTime(at)
		wg.Add(len(fs))

		for _, f := range fs {
			go func(f func()) {
				time.Sleep(ts.Sub(time.Now()))
				f()
				wg.Done()
			}(f)
		}
	}

	wg.Wait()
}

// Alertmanager encapsulates an Alertmanager process and allows
// declaring alerts being pushed to it at fixed points in time.
type Alertmanager struct {
	t    *AcceptanceTest
	opts *AcceptanceOpts

	addr     string
	client   alertmanager.Client
	cmd      *exec.Cmd
	confFile *os.File
}

// Start the alertmanager and wait until it is ready to receive.
func (am *Alertmanager) Start() {
	if err := am.cmd.Start(); err != nil {
		am.t.Fatalf("Starting alertmanager failed: %s", err)
	}

	time.Sleep(100 * time.Millisecond)
}

// kill the underlying Alertmanager process and remove intermediate data.
func (am *Alertmanager) Terminate() {
	syscall.Kill(am.cmd.Process.Pid, syscall.SIGTERM)
}

// Reload sends the reloading signal to the Alertmanager process.
func (am *Alertmanager) Reload() {
	syscall.Kill(am.cmd.Process.Pid, syscall.SIGHUP)
}

func (am *Alertmanager) cleanup() {
	os.RemoveAll(am.confFile.Name())
}

// Push declares alerts that are to be pushed to the Alertmanager
// server at a relative point in time.
func (am *Alertmanager) Push(at float64, alerts ...*TestAlert) {
	var nas model.Alerts
	for _, a := range alerts {
		nas = append(nas, a.nativeAlert(am.opts))
	}

	alertAPI := alertmanager.NewAlertAPI(am.client)

	am.t.Do(at, func() {
		if err := alertAPI.Push(context.Background(), nas...); err != nil {
			am.t.Error(err)
		}
	})
}

// SetSilence updates or creates the given Silence.
func (am *Alertmanager) SetSilence(at float64, sil *TestSilence) {
	silences := alertmanager.NewSilenceAPI(am.client)

	am.t.Do(at, func() {
		sid, err := silences.Set(context.Background(), sil.nativeSilence(am.opts))
		if err != nil {
			am.t.Error(err)
			return
		}
		sil.ID = sid
	})
}

// DelSilence deletes the silence with the sid at the given time.
func (am *Alertmanager) DelSilence(at float64, sil *TestSilence) {
	silences := alertmanager.NewSilenceAPI(am.client)

	am.t.Do(at, func() {
		if err := silences.Del(context.Background(), sil.ID); err != nil {
			am.t.Error(err)
		}
	})
}

// UpdateConfig rewrites the configuration file for the Alertmanager. It does not
// initiate config reloading.
func (am *Alertmanager) UpdateConfig(conf string) {
	if _, err := am.confFile.WriteString(conf); err != nil {
		am.t.Error(err)
		return
	}
	if err := am.confFile.Sync(); err != nil {
		am.t.Error(err)
		return
	}
}
