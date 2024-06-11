package dotnet

import (
	"path"
	"time"
)

// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/consul-template/signals"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/lib/cgroupslib"
	"github.com/hashicorp/nomad/drivers/shared/capabilities"
	"github.com/hashicorp/nomad/drivers/shared/eventer"
	"github.com/hashicorp/nomad/drivers/shared/executor"
	"github.com/hashicorp/nomad/drivers/shared/resolvconf"
	"github.com/hashicorp/nomad/helper/pluginutils/loader"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/hashicorp/nomad/plugins/drivers/fsisolation"
	"github.com/hashicorp/nomad/plugins/drivers/utils"
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
	pstructs "github.com/hashicorp/nomad/plugins/shared/structs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	// pluginName is the name of the plugin
	pluginName = "dotnet"

	// fingerprintPeriod is the interval at which the driver will send fingerprint responses
	fingerprintPeriod = 30 * time.Second

	// The key populated in Node Attributes to indicate presence of the Dotnet driver
	driverAttr           = "driver.dotnet"
	driverVersionAttr    = "driver.dotnet.version"
	driverRuntimeAttr    = "driver.dotnet.runtime"
	driverWebRuntimeAttr = "driver.dotnet.web.runtime"

	// taskHandleVersion is the version of task handle which this driver sets
	// and understands how to decode driver state
	taskHandleVersion = 1
)

var (
	// PluginID is the dotnet plugin metadata registered in the plugin
	// catalog.
	PluginID = loader.PluginID{
		Name:       pluginName,
		PluginType: base.PluginTypeDriver,
	}

	// PluginConfig is the dotnet driver factory function registered in the
	// plugin catalog.
	PluginConfig = &loader.InternalPluginConfig{
		Config:  map[string]interface{}{},
		Factory: func(ctx context.Context, l hclog.Logger) interface{} { return NewDriver(ctx, l) },
	}

	// pluginInfo is the response returned for the PluginInfo RPC
	pluginInfo = &base.PluginInfoResponse{
		Type:              base.PluginTypeDriver,
		PluginApiVersions: []string{drivers.ApiVersion010},
		PluginVersion:     "0.1.0",
		Name:              pluginName,
	}

	// configSpec is the hcl specification returned by the ConfigSchema RPC
	configSpec = hclspec.NewObject(map[string]*hclspec.Spec{
		"default_pid_mode": hclspec.NewDefault(
			hclspec.NewAttr("default_pid_mode", "string", false),
			hclspec.NewLiteral(`"private"`),
		),
		"default_ipc_mode": hclspec.NewDefault(
			hclspec.NewAttr("default_ipc_mode", "string", false),
			hclspec.NewLiteral(`"private"`),
		),
		"allow_caps": hclspec.NewDefault(
			hclspec.NewAttr("allow_caps", "list(string)", false),
			hclspec.NewLiteral(capabilities.HCLSpecLiteral),
		),
	})

	// taskConfigSpec is the hcl specification for the driver config section of
	// a taskConfig within a job. It is returned in the TaskConfigSchema RPC
	taskConfigSpec = hclspec.NewObject(map[string]*hclspec.Spec{
		// It's required for either `class` or `dll_path` to be set,
		// but that's not expressable in hclspec.  Marking both as optional
		// and setting checking explicitly later
		"dll_path": hclspec.NewAttr("dll_path", "string", false),
		"gc": hclspec.NewBlock("gc", false, hclspec.NewObject(map[string]*hclspec.Spec{
			"enable":               hclspec.NewAttr("enable", "bool", false),
			"concurrent":           hclspec.NewAttr("concurrent", "bool", false),
			"heap_limit":           hclspec.NewAttr("heap_limit", "number", false),
			"heap_limit_percent":   hclspec.NewAttr("heap_limit_percent", "number", false),
			"no_affinity":          hclspec.NewAttr("no_affinity", "bool", false),
			"heap_affinity_mask":   hclspec.NewAttr("heap_affinity_mask", "number", false),
			"heap_affinity_ranges": hclspec.NewAttr("heap_affinity_ranges", "string", false),
			"cpu_group":            hclspec.NewAttr("cpu_group", "bool", false),
			"high_mem_percent":     hclspec.NewAttr("high_mem_percent", "number", false),
			"retain_vm":            hclspec.NewAttr("retain_vm", "bool", false),
		})),
		"globalization": hclspec.NewBlock("globalization", false, hclspec.NewObject(map[string]*hclspec.Spec{
			"invariant":                hclspec.NewAttr("invariant", "bool", false),
			"use_nls":                  hclspec.NewAttr("use_nls", "bool", false),
			"predefined_cultures_only": hclspec.NewAttr("predefined_cultures_only", "bool", false),
		})),
		"threading": hclspec.NewBlock("threading", false, hclspec.NewObject(map[string]*hclspec.Spec{
			"min_threads":             hclspec.NewAttr("min_threads", "number", false),
			"max_threads":             hclspec.NewAttr("max_threads", "number", false),
			"windows_thread_pool":     hclspec.NewAttr("windows_thread_pool", "bool", false),
			"enable_autorelease_pool": hclspec.NewAttr("enable_autorelease_pool", "bool", false),
		})),
		"args":     hclspec.NewAttr("args", "list(string)", false),
		"pid_mode": hclspec.NewAttr("pid_mode", "string", false),
		"ipc_mode": hclspec.NewAttr("ipc_mode", "string", false),
		"cap_add":  hclspec.NewAttr("cap_add", "list(string)", false),
		"cap_drop": hclspec.NewAttr("cap_drop", "list(string)", false),
	})

	// driverCapabilities is returned by the Capabilities RPC and indicates what
	// optional features this driver supports
	driverCapabilities = &drivers.Capabilities{
		SendSignals: false,
		Exec:        false,
		FSIsolation: fsisolation.None,
		NetIsolationModes: []drivers.NetIsolationMode{
			drivers.NetIsolationModeHost,
			drivers.NetIsolationModeGroup,
		},
		MountConfigs: drivers.MountConfigSupportNone,
	}

	_ drivers.DriverPlugin = (*Driver)(nil)
)

func init() {
	if runtime.GOOS == "linux" {
		driverCapabilities.FSIsolation = fsisolation.Chroot
		driverCapabilities.MountConfigs = drivers.MountConfigSupportAll
	}
}

// Config is the driver configuration set by the SetConfig RPC call
type Config struct {
	// DefaultModePID is the default PID isolation set for all tasks using
	// exec-based task drivers.
	DefaultModePID string `codec:"default_pid_mode"`

	// DefaultModeIPC is the default IPC isolation set for all tasks using
	// exec-based task drivers.
	DefaultModeIPC string `codec:"default_ipc_mode"`

	// AllowCaps configures which Linux Capabilities are enabled for tasks
	// running on this node.
	AllowCaps []string `codec:"allow_caps"`
}

func (c *Config) validate() error {
	switch c.DefaultModePID {
	case executor.IsolationModePrivate, executor.IsolationModeHost:
	default:
		return fmt.Errorf("default_pid_mode must be %q or %q, got %q", executor.IsolationModePrivate, executor.IsolationModeHost, c.DefaultModePID)
	}

	switch c.DefaultModeIPC {
	case executor.IsolationModePrivate, executor.IsolationModeHost:
	default:
		return fmt.Errorf("default_ipc_mode must be %q or %q, got %q", executor.IsolationModePrivate, executor.IsolationModeHost, c.DefaultModeIPC)
	}

	badCaps := capabilities.Supported().Difference(capabilities.New(c.AllowCaps))
	if !badCaps.Empty() {
		return fmt.Errorf("allow_caps configured with capabilities not supported by system: %s", badCaps)
	}

	return nil
}

// TaskConfig is the driver configuration of a taskConfig within a job
type TaskConfig struct {

	// DotnetPath indicates where a dll file is found.
	DotnetPath string `codec:"dll_path"`

	// RuntimeOptions are arguments to pass to the dotnet
	GC *GcConfig `codec:"gc"`

	Globalization *GlobalizationConfig `codec:"globalization"`

	Threading *ThreadingConfig `codec:"threading"`

	// Args are extra arguments to dotnet executable
	Args []string `codec:"args"`

	// ModePID indicates whether PID namespace isolation is enabled for the task.
	// Must be "private" or "host" if set.
	ModePID string `codec:"pid_mode"`

	// ModeIPC indicates whether IPC namespace isolation is enabled for the task.
	// Must be "private" or "host" if set.
	ModeIPC string `codec:"ipc_mode"`

	// CapAdd is a set of linux capabilities to enable.
	CapAdd []string `codec:"cap_add"`

	// CapDrop is a set of linux capabilities to disable.
	CapDrop []string `codec:"cap_drop"`
}

func (tc *TaskConfig) validate() error {
	switch tc.ModePID {
	case "", executor.IsolationModePrivate, executor.IsolationModeHost:
	default:
		return fmt.Errorf("pid_mode must be %q or %q, got %q", executor.IsolationModePrivate, executor.IsolationModeHost, tc.ModePID)

	}

	switch tc.ModeIPC {
	case "", executor.IsolationModePrivate, executor.IsolationModeHost:
	default:
		return fmt.Errorf("ipc_mode must be %q or %q, got %q", executor.IsolationModePrivate, executor.IsolationModeHost, tc.ModeIPC)
	}

	supported := capabilities.Supported()
	badAdds := supported.Difference(capabilities.New(tc.CapAdd))
	if !badAdds.Empty() {
		return fmt.Errorf("cap_add configured with capabilities not supported by system: %s", badAdds)
	}
	badDrops := supported.Difference(capabilities.New(tc.CapDrop))
	if !badDrops.Empty() {
		return fmt.Errorf("cap_drop configured with capabilities not supported by system: %s", badDrops)
	}

	return nil
}

// TaskState is the state which is encoded in the handle returned in
// StartTask. This information is needed to rebuild the taskConfig state and handler
// during recovery.
type TaskState struct {
	ReattachConfig *pstructs.ReattachConfig
	TaskConfig     *drivers.TaskConfig
	Pid            int
	StartedAt      time.Time
}

// Driver is a driver for running images via Dotnet
type Driver struct {
	// eventer is used to handle multiplexing of TaskEvents calls such that an
	// event can be broadcast to all callers
	eventer *eventer.Eventer

	// config is the driver configuration set by the SetConfig RPC
	config Config

	// tasks is the in memory datastore mapping taskIDs to taskHandle
	tasks *taskStore

	// ctx is the context for the driver. It is passed to other subsystems to
	// coordinate shutdown
	ctx context.Context

	// nomadConf is the client agent's configuration
	nomadConfig *base.ClientDriverConfig

	// logger will log to the Nomad agent
	logger hclog.Logger
}

func NewDriver(ctx context.Context, logger hclog.Logger) drivers.DriverPlugin {
	logger = logger.Named(pluginName)
	return &Driver{
		eventer: eventer.NewEventer(ctx, logger),
		tasks:   newTaskStore(),
		ctx:     ctx,
		logger:  logger,
	}
}

func (d *Driver) PluginInfo() (*base.PluginInfoResponse, error) {
	return pluginInfo, nil
}

func (d *Driver) ConfigSchema() (*hclspec.Spec, error) {
	return configSpec, nil
}

func (d *Driver) SetConfig(cfg *base.Config) error {
	// unpack, validate, and set agent plugin config
	var config Config
	if len(cfg.PluginConfig) != 0 {
		if err := base.MsgPackDecode(cfg.PluginConfig, &config); err != nil {
			return err
		}
	}
	if err := config.validate(); err != nil {
		return err
	}
	d.config = config

	if cfg.AgentConfig != nil {
		d.nomadConfig = cfg.AgentConfig.Driver
	}
	return nil
}

func (d *Driver) TaskConfigSchema() (*hclspec.Spec, error) {
	return taskConfigSpec, nil
}

func (d *Driver) Capabilities() (*drivers.Capabilities, error) {
	return driverCapabilities, nil
}

func (d *Driver) Fingerprint(ctx context.Context) (<-chan *drivers.Fingerprint, error) {
	ch := make(chan *drivers.Fingerprint)
	go d.handleFingerprint(ctx, ch)
	return ch, nil
}

func (d *Driver) handleFingerprint(ctx context.Context, ch chan *drivers.Fingerprint) {
	ticker := time.NewTimer(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			ticker.Reset(fingerprintPeriod)
			ch <- d.buildFingerprint()
		}
	}
}

func (d *Driver) buildFingerprint() *drivers.Fingerprint {
	fp := &drivers.Fingerprint{
		Attributes:        map[string]*pstructs.Attribute{},
		Health:            drivers.HealthStateHealthy,
		HealthDescription: drivers.DriverHealthy,
	}

	if runtime.GOOS == "linux" {
		// Only enable if w are root and cgroups are mounted when running on linux system
		if !utils.IsUnixRoot() {
			fp.Health = drivers.HealthStateUndetected
			fp.HealthDescription = drivers.DriverRequiresRootMessage
			return fp
		}

		if cgroupslib.GetMode() == cgroupslib.OFF {
			fp.Health = drivers.HealthStateUnhealthy
			fp.HealthDescription = drivers.NoCgroupMountMessage
			return fp
		}
	}

	version, err := VersionInfo()
	if err != nil {
		// return no error, as it isn't an error to not find dotnet, it just means we
		// can't use it.
		fp.Health = drivers.HealthStateUndetected
		fp.HealthDescription = ""
		return fp
	}

	fp.Attributes[driverAttr] = pstructs.NewBoolAttribute(true)
	fp.Attributes[driverVersionAttr] = pstructs.NewStringAttribute(version)

	return fp
}

func (d *Driver) RecoverTask(handle *drivers.TaskHandle) error {
	if handle == nil {
		return fmt.Errorf("handle cannot be nil")
	}

	// If already attached to handle there's nothing to recover.
	if _, ok := d.tasks.Get(handle.Config.ID); ok {
		d.logger.Debug("nothing to recover; task already exists",
			"task_id", handle.Config.ID,
			"task_name", handle.Config.Name,
		)
		return nil
	}

	var taskState TaskState
	if err := handle.GetDriverState(&taskState); err != nil {
		d.logger.Error("failed to decode taskConfig state from handle", "error", err, "task_id", handle.Config.ID)
		return fmt.Errorf("failed to decode taskConfig state from handle: %v", err)
	}

	plugRC, err := pstructs.ReattachConfigToGoPlugin(taskState.ReattachConfig)
	if err != nil {
		d.logger.Error("failed to build ReattachConfig from taskConfig state", "error", err, "task_id", handle.Config.ID)
		return fmt.Errorf("failed to build ReattachConfig from taskConfig state: %v", err)
	}

	execImpl, pluginClient, err := executor.ReattachToExecutor(
		plugRC,
		d.logger.With("task_name", handle.Config.Name, "alloc_id", handle.Config.AllocID),
		d.nomadConfig.Topology.Compute(),
	)

	if err != nil {
		d.logger.Error("failed to reattach to executor", "error", err, "task_id", handle.Config.ID)
		return fmt.Errorf("failed to reattach to executor: %v", err)
	}

	h := &taskHandle{
		exec:         execImpl,
		pid:          taskState.Pid,
		pluginClient: pluginClient,
		taskConfig:   taskState.TaskConfig,
		procState:    drivers.TaskStateRunning,
		startedAt:    taskState.StartedAt,
		exitResult:   &drivers.ExitResult{},
		logger:       d.logger,
	}

	d.tasks.Set(taskState.TaskConfig.ID, h)

	go h.run()
	return nil
}

func (d *Driver) StartTask(cfg *drivers.TaskConfig) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	if _, ok := d.tasks.Get(cfg.ID); ok {
		return nil, nil, fmt.Errorf("task with ID %q already started", cfg.ID)
	}

	var driverConfig TaskConfig
	if err := cfg.DecodeDriverConfig(&driverConfig); err != nil {
		return nil, nil, fmt.Errorf("failed to decode driver config: %v", err)
	}

	if err := driverConfig.validate(); err != nil {
		return nil, nil, fmt.Errorf("failed driver config validation: %v", err)
	}

	if driverConfig.DotnetPath == "" {
		return nil, nil, fmt.Errorf("dll_path must be specified")
	}

	absPath, err := GetAbsolutePath("dotnet")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find dotnet binary: %s", err)
	}

	args := dotnetCmdArgs(driverConfig)

	var fileConfig = new(ConfigFile)
	addGcConfig(driverConfig.GC, fileConfig)
	addGlobalizationConfig(driverConfig.Globalization, fileConfig)
	addThreadingConfig(driverConfig.Threading, fileConfig)

	data, _ := json.Marshal(fileConfig)
	//return nil, nil, fmt.Errorf(path.Join(os.Getenv("NOMAD_TASK_DIR"), "runtimeConfig.json"))
	fo, err := os.Create(path.Join(filepath.Dir(driverConfig.DotnetPath), "runtimeConfig.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create runtimeConfig.json: %v", err)
	}
	if _, err := fo.Write(data); err != nil {
		return nil, nil, fmt.Errorf("failed to write runtimeConfig.json: %v", err)
	}
	defer func(fo *os.File) {
		err := fo.Close()
		if err != nil {
			return
		}
	}(fo)

	d.logger.Info("starting dotnet task", "driver_cfg", hclog.Fmt("%+v", driverConfig), "args", args)

	handle := drivers.NewTaskHandle(taskHandleVersion)
	handle.Config = cfg

	pluginLogFile := filepath.Join(cfg.TaskDir().Dir, "executor.out")
	executorConfig := &executor.ExecutorConfig{
		LogFile:     pluginLogFile,
		LogLevel:    "debug",
		FSIsolation: driverCapabilities.FSIsolation == fsisolation.Chroot,
		Compute:     d.nomadConfig.Topology.Compute(),
	}

	execVar, pluginClient, err := executor.CreateExecutor(
		d.logger.With("task_name", handle.Config.Name, "alloc_id", handle.Config.AllocID),
		d.nomadConfig, executorConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create executor: %v", err)
	}

	user := cfg.User
	if user == "" {
		user = "nobody"
	}

	if cfg.DNS != nil {
		dnsMount, err := resolvconf.GenerateDNSMount(cfg.TaskDir().Dir, cfg.DNS)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to build mount for resolv.conf: %v", err)
		}
		cfg.Mounts = append(cfg.Mounts, dnsMount)
	}

	caps, err := capabilities.Calculate(
		capabilities.NomadDefaults(), d.config.AllowCaps, driverConfig.CapAdd, driverConfig.CapDrop,
	)
	if err != nil {
		return nil, nil, err
	}
	d.logger.Debug("task capabilities", "capabilities", caps)

	execCmd := &executor.ExecCommand{
		Cmd:              absPath,
		Args:             args,
		Env:              cfg.EnvList(),
		User:             user,
		ResourceLimits:   true,
		Resources:        cfg.Resources,
		TaskDir:          cfg.TaskDir().Dir,
		StdoutPath:       cfg.StdoutPath,
		StderrPath:       cfg.StderrPath,
		Mounts:           cfg.Mounts,
		Devices:          cfg.Devices,
		NetworkIsolation: cfg.NetworkIsolation,
		ModePID:          executor.IsolationMode(d.config.DefaultModePID, driverConfig.ModePID),
		ModeIPC:          executor.IsolationMode(d.config.DefaultModeIPC, driverConfig.ModeIPC),
		Capabilities:     caps,
	}

	ps, err := execVar.Launch(execCmd)
	if err != nil {
		pluginClient.Kill()
		return nil, nil, fmt.Errorf("failed to launch command with executor: %v", err)
	}

	h := &taskHandle{
		exec:         execVar,
		pid:          ps.Pid,
		pluginClient: pluginClient,
		taskConfig:   cfg,
		procState:    drivers.TaskStateRunning,
		startedAt:    time.Now().Round(time.Millisecond),
		logger:       d.logger,
	}

	driverState := TaskState{
		ReattachConfig: pstructs.ReattachConfigFromGoPlugin(pluginClient.ReattachConfig()),
		Pid:            ps.Pid,
		TaskConfig:     cfg,
		StartedAt:      h.startedAt,
	}

	if err := handle.SetDriverState(&driverState); err != nil {
		d.logger.Error("failed to start task, error setting driver state", "error", err)
		err := execVar.Shutdown("", 0)
		if err != nil {
			return nil, nil, err
		}
		pluginClient.Kill()
		return nil, nil, fmt.Errorf("failed to set driver state: %v", err)
	}

	d.tasks.Set(cfg.ID, h)
	go h.run()
	return handle, nil, nil
}

func dotnetCmdArgs(driverConfig TaskConfig) []string {
	var args []string

	// Add the dll
	if driverConfig.DotnetPath != "" {
		args = append(args, driverConfig.DotnetPath)
	}

	// Add any args
	if len(driverConfig.Args) != 0 {
		args = append(args, driverConfig.Args...)
	}

	return args
}

func (d *Driver) WaitTask(ctx context.Context, taskID string) (<-chan *drivers.ExitResult, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	ch := make(chan *drivers.ExitResult)
	go d.handleWait(ctx, handle, ch)

	return ch, nil
}

func (d *Driver) handleWait(ctx context.Context, handle *taskHandle, ch chan *drivers.ExitResult) {
	defer close(ch)
	var result *drivers.ExitResult
	ps, err := handle.exec.Wait(ctx)
	if err != nil {
		result = &drivers.ExitResult{
			Err: fmt.Errorf("executor: error waiting on process: %v", err),
		}
	} else {
		result = &drivers.ExitResult{
			ExitCode:  ps.ExitCode,
			Signal:    ps.Signal,
			OOMKilled: ps.OOMKilled,
		}
	}

	select {
	case <-ctx.Done():
		return
	case <-d.ctx.Done():
		return
	case ch <- result:
	}
}

func (d *Driver) StopTask(taskID string, timeout time.Duration, signal string) error {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if err := handle.exec.Shutdown(signal, timeout); err != nil {
		if handle.pluginClient.Exited() {
			return nil
		}
		return fmt.Errorf("executor Shutdown failed: %v", err)
	}

	return nil
}

func (d *Driver) DestroyTask(taskID string, force bool) error {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if handle.IsRunning() && !force {
		return fmt.Errorf("cannot destroy running task")
	}

	if !handle.pluginClient.Exited() {
		if err := handle.exec.Shutdown("", 0); err != nil {
			handle.logger.Error("destroying executor failed", "error", err)
		}

		handle.pluginClient.Kill()
	}

	d.tasks.Delete(taskID)
	return nil
}

func (d *Driver) InspectTask(taskID string) (*drivers.TaskStatus, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	return handle.TaskStatus(), nil
}

func (d *Driver) TaskStats(ctx context.Context, taskID string, interval time.Duration) (<-chan *drivers.TaskResourceUsage, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	return handle.exec.Stats(ctx, interval)
}

func (d *Driver) TaskEvents(ctx context.Context) (<-chan *drivers.TaskEvent, error) {
	return d.eventer.TaskEvents(ctx)
}

func (d *Driver) SignalTask(taskID string, signal string) error {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	sig := os.Interrupt
	if s, ok := signals.SignalLookup[signal]; ok {
		sig = s
	} else {
		d.logger.Warn("unknown signal to send to task, using SIGINT instead", "signal", signal, "task_id", handle.taskConfig.ID)

	}
	return handle.exec.Signal(sig)
}

func (d *Driver) ExecTask(taskID string, cmd []string, timeout time.Duration) (*drivers.ExecTaskResult, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("error cmd must have at least one value")
	}
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	out, exitCode, err := handle.exec.Exec(time.Now().Add(timeout), cmd[0], cmd[1:])
	if err != nil {
		return nil, err
	}

	return &drivers.ExecTaskResult{
		Stdout: out,
		ExitResult: &drivers.ExitResult{
			ExitCode: exitCode,
		},
	}, nil
}

var _ drivers.ExecTaskStreamingRawDriver = (*Driver)(nil)

func (d *Driver) ExecTaskStreamingRaw(ctx context.Context,
	taskID string,
	command []string,
	tty bool,
	stream drivers.ExecTaskStream) error {

	if len(command) == 0 {
		return fmt.Errorf("error cmd must have at least one value")
	}
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	return handle.exec.ExecStreaming(ctx, command, tty, stream)
}

// GetAbsolutePath returns the absolute path of the passed binary by resolving
// it in the path and following symlinks.
func GetAbsolutePath(bin string) (string, error) {
	lp, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path to %q executable: %v", bin, err)
	}

	return filepath.EvalSymlinks(lp)
}
