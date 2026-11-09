package temporalqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// registrationRegistry protects protocol names while preserving SDK registration options.
type registrationRegistry struct {
	mu     sync.Mutex
	inner  worker.Registry
	names  map[string]bool
	sealed bool
}

// claim rejects duplicate or reserved registrations even when SDK duplicate checks are disabled.
func (r *registrationRegistry) claim(kind, name string) {
	if r.sealed || name == "" || strings.HasPrefix(name, "goforj.queue.") || r.names[kind+":"+name] {
		panic("Temporal registration conflicts with the resource registry")
	}
	r.names[kind+":"+name] = true
}

// functionName reproduces the SDK default function and bound-method registration name.
func functionName(fn any) string {
	value := reflect.ValueOf(fn)
	if !value.IsValid() || value.Kind() != reflect.Func || value.IsNil() {
		panic("invalid Temporal registration")
	}
	name := runtime.FuncForPC(value.Pointer()).Name()
	name = strings.TrimSuffix(name, "-fm")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// RegisterWorkflow preserves SDK defaults through the checked registration path.
func (r *registrationRegistry) RegisterWorkflow(fn interface{}) {
	r.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{})
}

// RegisterWorkflowWithOptions records the effective SDK workflow type before registering.
func (r *registrationRegistry) RegisterWorkflowWithOptions(fn interface{}, opts workflow.RegisterOptions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := opts.Name
	if name == "" {
		name = functionName(fn)
	}
	r.claim("workflow", name)
	if opts.Name == "" {
		opts.Name = name
	}
	r.inner.RegisterWorkflowWithOptions(fn, opts)
}

// RegisterDynamicWorkflow rejects handlers capable of shadowing framework protocol names.
func (r *registrationRegistry) RegisterDynamicWorkflow(interface{}, workflow.DynamicRegisterOptions) {
	panic("dynamic Temporal workflows are unsupported")
}

// RegisterActivity preserves SDK defaults through the checked registration path.
func (r *registrationRegistry) RegisterActivity(fn interface{}) {
	r.RegisterActivityWithOptions(fn, activity.RegisterOptions{})
}

// RegisterActivityWithOptions includes each exported struct method and explicit name prefix.
func (r *registrationRegistry) RegisterActivityWithOptions(fn interface{}, opts activity.RegisterOptions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := reflect.ValueOf(fn)
	if _, ok := fn.(payloadActivity); ok {
		r.claim("activity", opts.Name)
		r.inner.RegisterActivityWithOptions(fn, opts)
		return
	}
	if value.IsValid() && value.Kind() == reflect.Ptr && value.Type().Elem().Kind() == reflect.Struct {
		for i := 0; i < value.NumMethod(); i++ {
			method := value.Type().Method(i)
			if opts.SkipInvalidStructFunctions && !validActivityMethod(method.Type) {
				continue
			}
			r.claim("activity", opts.Name+method.Name)
		}
	} else {
		name := opts.Name
		if name == "" {
			name = functionName(fn)
		}
		r.claim("activity", name)
		if opts.Name == "" {
			opts.Name = name
		}
	}
	r.inner.RegisterActivityWithOptions(fn, opts)
}

// RegisterDynamicActivity rejects handlers capable of shadowing framework deliveries.
func (r *registrationRegistry) RegisterDynamicActivity(interface{}, activity.DynamicRegisterOptions) {
	panic("dynamic Temporal activities are unsupported")
}

// RegisterNexusService records the actual SDK service name without taking worker ownership.
func (r *registrationRegistry) RegisterNexusService(service *nexus.Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claim("nexus", service.Name)
	r.inner.RegisterNexusService(service)
}

// RegistrationFingerprint returns a stable catalog digest without handler addresses or payloads.
// Logical handler names are sampled on every call so late queue registration is visible.
func (b *backend) RegistrationFingerprint() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := append([]string{jobWorkflowType, coordinatorWorkflowType, deliveryActivityType}, b.nativeNames...)
	for name := range b.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return hex.EncodeToString(sum[:])
}

// validActivityMethod mirrors the pinned SDK's public SkipInvalidStructFunctions registration behavior.
func validActivityMethod(fn reflect.Type) bool {
	if fn.NumOut() < 1 || fn.NumOut() > 2 || !fn.Out(fn.NumOut()-1).Implements(reflect.TypeFor[error]()) {
		return false
	}
	for i := 0; i < fn.NumIn() && i < 2; i++ {
		if fn.In(i) == reflect.TypeFor[workflow.Context]() {
			return false
		}
	}
	if fn.NumOut() == 2 {
		switch fn.Out(0).Kind() {
		case reflect.Func, reflect.Chan, reflect.UnsafePointer:
			return false
		}
	}
	return true
}
