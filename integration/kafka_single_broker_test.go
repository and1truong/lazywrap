//go:build integration && unix

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type lockedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.String()
}

func TestKafkaSingleBrokerRoundTrip(t *testing.T) {
	kafkaHome := os.Getenv("KAFKA_HOME")
	if kafkaHome == "" {
		t.Skip("set KAFKA_HOME to run the Kafka integration test")
	}
	var err error
	kafkaHome, err = filepath.Abs(kafkaHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"kafka-storage.sh", "kafka-server-start.sh", "kafka-topics.sh", "kafka-console-producer.sh", "kafka-console-consumer.sh"} {
		if _, err := os.Stat(filepath.Join(kafkaHome, "bin", script)); err != nil {
			t.Fatalf("Kafka script %s: %v", script, err)
		}
	}

	ports := freePorts(t, 4)
	httpPort, proxyPort, brokerPort, controllerPort := ports[0], ports[1], ports[2], ports[3]
	tempDir := t.TempDir()
	propertiesPath := filepath.Join(tempDir, "server.properties")
	properties := fmt.Sprintf(`process.roles=broker,controller
node.id=1
controller.quorum.voters=1@127.0.0.1:%d
controller.listener.names=CONTROLLER
listeners=BROKER://127.0.0.1:%d,CONTROLLER://127.0.0.1:%d
advertised.listeners=BROKER://127.0.0.1:%d
listener.security.protocol.map=BROKER:PLAINTEXT,CONTROLLER:PLAINTEXT
inter.broker.listener.name=BROKER
log.dirs=%s
offsets.topic.replication.factor=1
transaction.state.log.replication.factor=1
transaction.state.log.min.isr=1
group.initial.rebalance.delay.ms=0
`, controllerPort, brokerPort, controllerPort, proxyPort, filepath.Join(tempDir, "kafka-logs"))
	if err := os.WriteFile(propertiesPath, []byte(properties), 0600); err != nil {
		t.Fatal(err)
	}

	clusterID := strings.TrimSpace(runKafka(t, kafkaHome, nil, "kafka-storage.sh", "random-uuid"))
	runKafka(t, kafkaHome, nil, "kafka-storage.sh", "format", "-t", clusterID, "-c", propertiesPath)

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	heronPath := filepath.Join(tempDir, "heron")
	build := exec.Command("go", "build", "-o", heronPath, "./cmd/heron")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build heron: %v\n%s", err, output)
	}

	configPath := filepath.Join(tempDir, "heron.json")
	config := map[string]any{
		"port":         httpPort,
		"startTimeout": "60s",
		"stopTimeout":  "30s",
		"apps": map[string]any{
			"kafka": map[string]any{
				"pwd":        kafkaHome,
				"launch":     shellQuote(filepath.Join(kafkaHome, "bin", "kafka-server-start.sh")) + " " + shellQuote(propertiesPath),
				"protocol":   "tcp",
				"listenPort": proxyPort,
				"port":       brokerPort,
				"idle":       "0",
			},
		},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}

	var logs lockedBuffer
	cmd := exec.Command(heronPath, "-c", configPath)
	cmd.Dir = repoRoot
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var processErr error
	go func() {
		processErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
			if processErr != nil {
				t.Logf("heron exited with %v", processErr)
			}
		case <-time.After(35 * time.Second):
			_ = cmd.Process.Kill()
			t.Log("forced heron shutdown after timeout")
		}
		if t.Failed() {
			t.Logf("heron logs:\n%s", logs.String())
		}
	}()

	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/", httpPort), done)
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", brokerPort), 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("Kafka started before the first proxy connection")
	}

	bootstrap := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	topic := "heron-integration"
	runKafka(t, kafkaHome, nil, "kafka-topics.sh", "--bootstrap-server", bootstrap, "--create", "--topic", topic, "--partitions", "1", "--replication-factor", "1")
	listed := runKafka(t, kafkaHome, nil, "kafka-topics.sh", "--bootstrap-server", bootstrap, "--list")
	if !strings.Contains(listed, topic) {
		t.Fatalf("topic list %q does not contain %q", listed, topic)
	}

	runKafka(t, kafkaHome, strings.NewReader("hello through heron\n"), "kafka-console-producer.sh", "--bootstrap-server", bootstrap, "--topic", topic)
	consumed := runKafka(t, kafkaHome, nil, "kafka-console-consumer.sh", "--bootstrap-server", bootstrap, "--topic", topic, "--from-beginning", "--max-messages", "1", "--timeout-ms", "15000")
	if !strings.Contains(consumed, "hello through heron") {
		t.Fatalf("consumer output = %q", consumed)
	}
}

func freePorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	ports := make([]int, 0, count)
	for range count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, listener := range listeners {
				_ = listener.Close()
			}
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return ports
}

func TestFreePortsAreDistinct(t *testing.T) {
	ports := freePorts(t, 4)
	seen := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		if _, exists := seen[port]; exists {
			t.Fatalf("duplicate ephemeral port %d in %v", port, ports)
		}
		seen[port] = struct{}{}
	}
}

func runKafka(t *testing.T, kafkaHome string, stdin *strings.Reader, script string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(kafkaHome, "bin", script), args...)
	cmd.Dir = kafkaHome
	cmd.Stdin = stdin
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", script, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func waitForHTTP(t *testing.T, endpoint string, processDone <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(endpoint)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		select {
		case <-processDone:
			t.Fatal("heron exited before becoming ready")
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("heron HTTP listener did not become ready")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
