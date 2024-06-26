package k3s

import (
	"context"
	"fmt"
	testHelper "github.com/ksctl/ksctl/test/helpers"
	"os"
	"sync"
	"testing"

	"github.com/ksctl/ksctl/pkg/logger"

	"github.com/ksctl/ksctl/internal/storage/types"

	localstate "github.com/ksctl/ksctl/internal/storage/local"
	"github.com/ksctl/ksctl/pkg/helpers"
	"github.com/ksctl/ksctl/pkg/helpers/consts"
	"github.com/ksctl/ksctl/pkg/resources"
	cloudControlRes "github.com/ksctl/ksctl/pkg/resources/controllers/cloud"
	"gotest.tools/v3/assert"
)

var (
	storeHA resources.StorageFactory

	fakeClient         *K3s
	dir                = fmt.Sprintf("%s ksctl-k3s-test", os.TempDir())
	fakeStateFromCloud cloudControlRes.CloudResourceState
)

func NewClientHelper(x cloudControlRes.CloudResourceState, storage resources.StorageFactory, m resources.Metadata, state *types.StorageDocument) *K3s {

	mainStateDocument = state
	mainStateDocument.K8sBootstrap = &types.KubernetesBootstrapState{}
	var err error
	mainStateDocument.K8sBootstrap.B.CACert, mainStateDocument.K8sBootstrap.B.EtcdCert, mainStateDocument.K8sBootstrap.B.EtcdKey, err = helpers.GenerateCerts(log, x.PrivateIPv4DataStores)
	if err != nil {
		return nil
	}

	mainStateDocument.K8sBootstrap.B.PublicIPs.ControlPlanes = x.IPv4ControlPlanes
	mainStateDocument.K8sBootstrap.B.PrivateIPs.ControlPlanes = x.PrivateIPv4ControlPlanes

	mainStateDocument.K8sBootstrap.B.PublicIPs.DataStores = x.IPv4DataStores
	mainStateDocument.K8sBootstrap.B.PrivateIPs.DataStores = x.PrivateIPv4DataStores

	mainStateDocument.K8sBootstrap.B.PublicIPs.WorkerPlanes = x.IPv4WorkerPlanes

	mainStateDocument.K8sBootstrap.B.PublicIPs.LoadBalancer = x.IPv4LoadBalancer
	mainStateDocument.K8sBootstrap.B.PrivateIPs.LoadBalancer = x.PrivateIPv4LoadBalancer
	mainStateDocument.K8sBootstrap.B.SSHInfo = x.SSHState

	return &K3s{mu: &sync.Mutex{}}
}

func TestMain(m *testing.M) {
	log = logger.NewDefaultLogger(-1, os.Stdout)
	log.SetPackageName("k3s")
	mainState := &types.StorageDocument{}
	if err := helpers.CreateSSHKeyPair(log, mainState); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
	fakeStateFromCloud = cloudControlRes.CloudResourceState{
		SSHState: cloudControlRes.SSHInfo{
			PrivateKey: mainState.SSHKeyPair.PrivateKey,
			UserName:   "fakeuser",
		},
		Metadata: cloudControlRes.Metadata{
			ClusterName: "fake",
			Provider:    consts.CloudAzure,
			Region:      "fake",
			ClusterType: consts.ClusterTypeHa,
		},
		// public IPs
		IPv4ControlPlanes: []string{"A.B.C.4", "A.B.C.5", "A.B.C.6"},
		IPv4DataStores:    []string{"A.B.C.3"},
		IPv4WorkerPlanes:  []string{"A.B.C.2"},
		IPv4LoadBalancer:  "A.B.C.1",

		// Private IPs
		PrivateIPv4ControlPlanes: []string{"192.168.X.7", "192.168.X.9", "192.168.X.10"},
		PrivateIPv4DataStores:    []string{"192.168.5.2"},
		PrivateIPv4LoadBalancer:  "192.168.X.1",
	}

	fakeClient = NewClientHelper(fakeStateFromCloud, storeHA, resources.Metadata{
		ClusterName:  "fake",
		Region:       "fake",
		Provider:     consts.CloudAzure,
		IsHA:         true,
		LogVerbosity: -1,
		LogWritter:   os.Stdout,
		NoCP:         7,
		NoDS:         5,
		NoWP:         10,
		K8sDistro:    consts.K8sK3s,
	}, &types.StorageDocument{})
	if fakeClient == nil {
		panic("unable to initialize")
	}

	storeHA = localstate.InitStorage(-1, os.Stdout)
	_ = storeHA.Setup(consts.CloudAzure, "fake", "fake", consts.ClusterTypeHa)
	_ = storeHA.Connect(context.TODO())

	_ = os.Setenv(string(consts.KsctlCustomDirEnabled), dir)
	_ = os.Setenv(string(consts.KsctlFakeFlag), "true")

	exitVal := m.Run()

	fmt.Println("Cleanup..")
	if err := os.RemoveAll(os.TempDir() + helpers.PathSeparator + "ksctl-k3s-test"); err != nil {
		panic(err)
	}

	os.Exit(exitVal)
}

func TestK3sDistro_Version(t *testing.T) {
	forTesting := map[string]bool{
		"1.27.4":  true,
		"1.26.7":  true,
		"1.25.12": true,
		"1.27.1":  true,
		"1.27.0":  false,
	}
	for ver, expected := range forTesting {
		if ok := isValidK3sVersion(ver); ok != expected {
			t.Fatalf("Expected for %s as %v but got %v", ver, expected, ok)
		}
	}
}

func TestScriptsControlplane(t *testing.T) {

	ver := []string{"1.26.1", "1.27"}
	privIP := []string{"9.9.9.9", "1.1.1.1"}
	dbEndpoint := getEtcdMemberIPFieldForControlplane(privIP)
	pubIP := []string{"192.16.9.2", "23.34.4.1"}
	ca, etcd, key := "-- CA_CERT --", "-- ETCD_CERT --", "-- ETCD_KEY --"

	sampleToken := "k3ssdcdsXXXYYYZZZ"

	t.Run("script for controlplane 0", func(t *testing.T) {

		t.Run("script without cni", func(t *testing.T) {

			for i := 0; i < len(ver); i++ {

				testHelper.HelperTestTemplate(
					t,
					[]resources.Script{
						getScriptForEtcdCerts(ca, etcd, key),
						{
							Name:           "Start K3s Controlplane-[0] without CNI",
							MaxRetries:     9,
							CanRetry:       true,
							ScriptExecutor: consts.LinuxBash,
							ShellScript: fmt.Sprintf(`
cat <<EOF > control-setup.sh
#!/bin/bash
curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL="%s" sh -s - server \
	--node-taint CriticalAddonsOnly=true:NoExecute \
	--datastore-endpoint "%s" \
	--datastore-cafile=/var/lib/etcd/ca.pem \
	--datastore-keyfile=/var/lib/etcd/etcd-key.pem \
	--datastore-certfile=/var/lib/etcd/etcd.pem \
	--flannel-backend=none \
	--disable-network-policy \
	--tls-san %s
EOF

sudo chmod +x control-setup.sh
sudo ./control-setup.sh
`, ver[i], dbEndpoint, pubIP[i]),
						},
					},
					func() resources.ScriptCollection { // Adjust the signature to match your needs
						return scriptCP_1WithoutCNI(ca, etcd, key, ver[i], privIP, pubIP[i])
					},
				)

			}
		})
		t.Run("script with cni", func(t *testing.T) {

			for i := 0; i < len(ver); i++ {
				testHelper.HelperTestTemplate(
					t,
					[]resources.Script{
						getScriptForEtcdCerts(ca, etcd, key),
						{
							Name:           "Start K3s Controlplane-[0] with CNI",
							MaxRetries:     9,
							CanRetry:       true,
							ScriptExecutor: consts.LinuxBash,
							ShellScript: fmt.Sprintf(`
cat <<EOF > control-setup.sh
#!/bin/bash
curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL="%s" sh -s - server \
	--node-taint CriticalAddonsOnly=true:NoExecute \
	--datastore-endpoint "%s" \
	--datastore-cafile=/var/lib/etcd/ca.pem \
	--datastore-keyfile=/var/lib/etcd/etcd-key.pem \
	--datastore-certfile=/var/lib/etcd/etcd.pem \
	--tls-san %s
EOF

sudo chmod +x control-setup.sh
sudo ./control-setup.sh
`, ver[i], dbEndpoint, pubIP[i]),
						},
					},
					func() resources.ScriptCollection { // Adjust the signature to match your needs
						return scriptCP_1(ca, etcd, key, ver[i], privIP, pubIP[i])
					},
				)
			}
		})
	})

	t.Run("script for controlplane 1..N", func(t *testing.T) {

		t.Run("script without cni", func(t *testing.T) {

			for i := 0; i < len(ver); i++ {

				testHelper.HelperTestTemplate(
					t,
					[]resources.Script{
						getScriptForEtcdCerts(ca, etcd, key),
						{
							Name:           "Start K3s Controlplane-[1..N] without CNI",
							MaxRetries:     9,
							CanRetry:       true,
							ScriptExecutor: consts.LinuxBash,
							ShellScript: fmt.Sprintf(`
cat <<EOF > control-setupN.sh
#!/bin/bash
curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL="%s" sh -s - server \
	--token %s \
	--datastore-endpoint "%s" \
	--datastore-cafile=/var/lib/etcd/ca.pem \
	--datastore-keyfile=/var/lib/etcd/etcd-key.pem \
	--datastore-certfile=/var/lib/etcd/etcd.pem \
	--node-taint CriticalAddonsOnly=true:NoExecute \
	--flannel-backend=none \
	--disable-network-policy \
	--tls-san %s
EOF

sudo chmod +x control-setupN.sh
sudo ./control-setupN.sh
`, ver[i], sampleToken, dbEndpoint, pubIP[i]),
						},
					},
					func() resources.ScriptCollection { // Adjust the signature to match your needs
						return scriptCP_NWithoutCNI(ca, etcd, key, ver[i], privIP, pubIP[i], sampleToken)
					},
				)
			}
		})
		t.Run("script with cni", func(t *testing.T) {

			for i := 0; i < len(ver); i++ {

				testHelper.HelperTestTemplate(
					t,
					[]resources.Script{
						getScriptForEtcdCerts(ca, etcd, key),
						{
							Name:           "Start K3s Controlplane-[1..N] with CNI",
							MaxRetries:     9,
							CanRetry:       true,
							ScriptExecutor: consts.LinuxBash,
							ShellScript: fmt.Sprintf(`
cat <<EOF > control-setupN.sh
#!/bin/bash
curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL="%s" sh -s - server \
	--token %s \
	--datastore-endpoint "%s" \
	--datastore-cafile=/var/lib/etcd/ca.pem \
	--datastore-keyfile=/var/lib/etcd/etcd-key.pem \
	--datastore-certfile=/var/lib/etcd/etcd.pem \
	--node-taint CriticalAddonsOnly=true:NoExecute \
	--tls-san %s
EOF

sudo chmod +x control-setupN.sh
sudo ./control-setupN.sh
`, ver[i], sampleToken, dbEndpoint, pubIP[i]),
						},
					},
					func() resources.ScriptCollection { // Adjust the signature to match your needs
						return scriptCP_N(ca, etcd, key, ver[i], privIP, pubIP[i], sampleToken)
					},
				)
			}
		})
	})

	t.Run("get k3s token", func(t *testing.T) {
		testHelper.HelperTestTemplate(
			t,
			[]resources.Script{
				{
					Name:           "Get k3s server token",
					CanRetry:       false,
					ScriptExecutor: consts.LinuxBash,
					ShellScript: `
sudo cat /var/lib/rancher/k3s/server/token
`,
				},
			},
			func() resources.ScriptCollection { // Adjust the signature to match your needs
				return scriptForK3sToken()
			},
		)
	})

	t.Run("get kubeconfig", func(t *testing.T) {
		testHelper.HelperTestTemplate(
			t,
			[]resources.Script{
				{
					Name:           "k3s kubeconfig",
					CanRetry:       false,
					ScriptExecutor: consts.LinuxBash,
					ShellScript: `
sudo cat /etc/rancher/k3s/k3s.yaml
`,
				},
			},
			func() resources.ScriptCollection { // Adjust the signature to match your needs
				return scriptKUBECONFIG()
			},
		)
	})

}

func TestSciprWorkerplane(t *testing.T) {
	ver := "1.18"
	token := "K#Sde43rew34"
	private := "192.20.3.3"

	t.Run("get kubeconfig", func(t *testing.T) {
		testHelper.HelperTestTemplate(
			t,
			[]resources.Script{
				{
					Name:           "Join the workerplane-[0..M]",
					CanRetry:       true,
					MaxRetries:     3,
					ScriptExecutor: consts.LinuxBash,
					ShellScript: fmt.Sprintf(`
cat <<EOF > worker-setup.sh
#!/bin/bash
curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL="%s" sh -s - agent --token %s --server https://%s:6443
EOF

sudo chmod +x worker-setup.sh
sudo ./worker-setup.sh
`, ver, token, private),
				},
			},
			func() resources.ScriptCollection { // Adjust the signature to match your needs
				return scriptWP(ver, private, token)
			},
		)
	})
}

func checkCurrentStateFile(t *testing.T) {

	raw, err := storeHA.Read()
	if err != nil {
		t.Fatalf("Unable to access statefile")
	}

	assert.DeepEqual(t, mainStateDocument, raw)
}

func TestOverallScriptsCreation(t *testing.T) {
	assert.Equal(t, fakeClient.Setup(storeHA, consts.OperationStateCreate), nil, "should be initlize the state")

	fakeClient.Version("1.27.1")

	checkCurrentStateFile(t)

	noCP := len(fakeStateFromCloud.IPv4ControlPlanes)
	noWP := len(fakeStateFromCloud.IPv4WorkerPlanes)

	fakeClient.CNI("flannel")
	for no := 0; no < noCP; no++ {
		err := fakeClient.ConfigureControlPlane(no, storeHA)
		if err != nil {
			t.Fatalf("Configure Controlplane unable to operate %v", err)
		}
	}

	for no := 0; no < noWP; no++ {
		err := fakeClient.JoinWorkerplane(no, storeHA)
		if err != nil {
			t.Fatalf("Configure Workerplane unable to operate %v", err)
		}
	}

}

func TestCNI(t *testing.T) {
	testCases := map[string]bool{
		string(consts.CNIFlannel): false,
		string(consts.CNICilium):  true,
	}

	for k, v := range testCases {
		got := fakeClient.CNI(k)
		assert.Equal(t, got, v, "missmatch")
	}
}

func TestGetEtcdMemberIPFieldForControlplane(t *testing.T) {
	ips := []string{"9.9.9.9", "1.1.1.1"}
	res1 := "https://9.9.9.9:2379,https://1.1.1.1:2379"
	assert.Equal(t, res1, getEtcdMemberIPFieldForControlplane(ips), "it should be equal")

	assert.Equal(t, "", getEtcdMemberIPFieldForControlplane([]string{}), "it should be equal")
}
