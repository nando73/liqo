package csrApprover

import (
	"context"
	"github.com/stretchr/testify/assert"
	certificatesv1beta1 "k8s.io/api/certificates/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	testclient "k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestNewNamespaceWithSuffix(t *testing.T) {
	//setup
	certificateToValidate := certificatesv1beta1.CertificateSigningRequest{
		TypeMeta: v1.TypeMeta{},
		ObjectMeta: v1.ObjectMeta{
			Name: "to_validate",
			Labels: map[string]string{
				"liqo.io/csr": "true",
			},
		},
		Spec:   certificatesv1beta1.CertificateSigningRequestSpec{},
		Status: certificatesv1beta1.CertificateSigningRequestStatus{},
	}

	c := testclient.NewSimpleClientset()
	_, err := c.CertificatesV1beta1().CertificateSigningRequests().Create(context.TODO(), &certificateToValidate, v1.CreateOptions{})
	if err != nil {
		t.Fail()
	}
	err = approveCSR(c, &certificateToValidate)
	if err != nil {
		t.Fail()
	}
	cert, err := c.CertificatesV1beta1().CertificateSigningRequests().Get(context.TODO(), "to_validate", v1.GetOptions{})
	if err != nil {
		t.Fail()
	}
	assert.NotNil(t, cert)
	assert.NotEmpty(t, cert.Status.Conditions)
	conditions := cert.Status.Conditions
	assert.Equal(t, conditions[0].Type, certificatesv1beta1.CertificateApproved)
	assert.Equal(t, conditions[0].Reason, "LiqoApproval")
	assert.Equal(t, conditions[0].Message, "This CSR was approved by Liqo Advertisement Operator")
}
