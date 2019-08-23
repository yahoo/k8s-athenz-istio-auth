package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/yahoo/athenz/clients/go/zms"
	"github.com/yahoo/k8s-athenz-istio-auth/test/integration/fixtures"
	"github.com/yahoo/k8s-athenz-istio-auth/test/integration/framework"
	"istio.io/api/rbac/v1alpha1"
	"istio.io/istio/pilot/pkg/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yahoo/k8s-athenz-istio-auth/pkg/istio/rbac/common"
	"time"
)

// TODO, logging for callback weird [controller.(*Controller).getCallbackHandler/controller.go] [func1]
// TODO, look into logrus function logging
// TODO, should we sync if athenz domain is not found but namespace is there and delete sr / srb? https://github.com/yahoo/k8s-athenz-istio-auth/blob/master/pkg/controller/controller.go#L163-L167
// TODO, figure out why the warnings disappeared
// TODO, make the integration go.mod point to the local authz controller as opposed to the version
// TODO, add for loop to wait for athenz domain created
// TODO, validate if role actually exists from policy / assertion
// TODO, add back build tags?
// TODO, figure out which role we're using

// TODO, make it work with update and check for actual resources
// TODO, add informer / indexer for get?
func rolloutAndValidate(t *testing.T, adPair *fixtures.AthenzDomainPair, update bool) {
	if update {
		currentAD, err := framework.Global.AthenzDomainClientset.AthenzV1().AthenzDomains().Get(adPair.AD.Name, v1.GetOptions{})
		assert.Nil(t, err, "error should be nil")

		adPair.AD.ResourceVersion = currentAD.ResourceVersion

		_, err = framework.Global.AthenzDomainClientset.AthenzV1().AthenzDomains().Update(adPair.AD)
		assert.Nil(t, err, "error should be nil")
	} else {
		_, err := framework.Global.AthenzDomainClientset.AthenzV1().AthenzDomains().Create(adPair.AD)
		assert.Nil(t, err, "should be nil")
	}

	// TODO, might need deep equal check here for rollout
	for {
		index := 0
		for _, curr := range adPair.ModelConfigs {
			got := framework.Global.IstioClientset.Get(curr.Type, curr.Name, curr.Namespace)
			if got != nil {
				index++
			}
		}

		if index == len(adPair.ModelConfigs) {
			break
		}
		time.Sleep(time.Second)
	}

	for _, curr := range adPair.ModelConfigs {
		got := framework.Global.IstioClientset.Get(curr.Type, curr.Name, curr.Namespace)
		assert.NotNil(t, got, "not nil")
		curr.ResourceVersion = got.ResourceVersion
		curr.CreationTimestamp = got.CreationTimestamp
		assert.Equal(t, curr, *got, "should be equal")
	}
}

func cleanup(t *testing.T, adPair *fixtures.AthenzDomainPair) {
	err := framework.Global.AthenzDomainClientset.AthenzV1().AthenzDomains().Delete(adPair.AD.Name, &v1.DeleteOptions{})
	assert.Nil(t, err, "nil")

	for _, curr := range adPair.ModelConfigs {
		err := framework.Global.IstioClientset.Delete(curr.Type, curr.Name, "athenz-domain")
		assert.Nil(t, err, "nil")
	}
}

// 1. Test case:
// Initial: No AD, SR, SRB existing
// Input Actions: Create AD with roles and policies
// Output: SR / SRB created with matching rules and bindings
func TestServiceRoleAndBindingCreation(t *testing.T) {
	adPair := fixtures.CreateAthenzDomain(&fixtures.Override{})
	rolloutAndValidate(t, adPair, false)
	cleanup(t, adPair)
}

// 1.1 Create SR only if there are no role members for SRB creation
// TODO, make sure srb is not there
func TestServiceRoleOnlyCreation(t *testing.T) {
	o := &fixtures.Override{
		ModifyAD: func(signedDomain *zms.SignedDomain) {
			signedDomain.Domain.Roles[0].RoleMembers = []*zms.RoleMember{}
		},
	}
	adPair := fixtures.CreateAthenzDomain(o)
	rolloutAndValidate(t, adPair, false)
	cleanup(t, adPair)
}

// 2. Test case:
// Initial: Existing AD, SR, SRB
// Input Actions: Update AD with additional roles, policies
// Output: SR / SRB updated with matching rules and bindings
func TestUpdateRoleAssertion(t *testing.T) {
	adPair := fixtures.CreateAthenzDomain(&fixtures.Override{})
	rolloutAndValidate(t, adPair, false)

	o := &fixtures.Override{
		ModifyAD: func(signedDomain *zms.SignedDomain) {
			allow := zms.ALLOW
			domainName := "athenz.domain"
			policy := &zms.Policy{
				Assertions: []*zms.Assertion{
					{
						Effect:   &allow,
						Action:   "GET",
						Role:     "athenz.domain:role.client-writer-role",
						Resource: "athenz.domain:svc.my-service-name",
					},
				},
				Name: zms.ResourceName(domainName + ":policy.admin"),
			}
			signedDomain.Domain.Policies.Contents.Policies = append(signedDomain.Domain.Policies.Contents.Policies, policy)
		},
		ModifySR: func(sr *v1alpha1.ServiceRole) {
			sr.Rules = append(sr.Rules, &v1alpha1.AccessRule{
				Methods: []string{
					"GET",
				},
				Services: []string{common.WildCardAll},
				Constraints: []*v1alpha1.AccessRule_Constraint{
					{
						Key: common.ConstraintSvcKey,
						Values: []string{
							"my-service-name",
						},
					},
				},
			})
		},
	}

	adPair = fixtures.CreateAthenzDomain(o)
	rolloutAndValidate(t, adPair, true)
	cleanup(t, adPair)
}

// 2.1 Update existing assertion / roleMember / action
func TestUpdateRoleMember(t *testing.T) {
	adPair := fixtures.CreateAthenzDomain(&fixtures.Override{})
	rolloutAndValidate(t, adPair, false)

	o := &fixtures.Override{
		ModifyAD: func(signedDomain *zms.SignedDomain) {
			roleMember := &zms.RoleMember{
				MemberName: zms.MemberName("user.bar"),
			}
			signedDomain.Domain.Roles[0].RoleMembers = append(signedDomain.Domain.Roles[0].RoleMembers, roleMember)
		},
		ModifySRB: func(srb *v1alpha1.ServiceRoleBinding) {
			srb.Subjects = append(srb.Subjects, &v1alpha1.Subject{
				User: "user/sa/bar",
			})
		},
	}

	adPair = fixtures.CreateAthenzDomain(o)
	rolloutAndValidate(t, adPair, true)
	cleanup(t, adPair)
}

// 2.2 Delete existing roleMember / assertion
func TestUpdateDeleteRoleMember(t *testing.T) {

	o := &fixtures.Override{
		ModifyAD: func(signedDomain *zms.SignedDomain) {
			roleMember := &zms.RoleMember{
				MemberName: zms.MemberName("user.bar"),
			}
			signedDomain.Domain.Roles[0].RoleMembers = append(signedDomain.Domain.Roles[0].RoleMembers, roleMember)
		},
		ModifySRB: func(srb *v1alpha1.ServiceRoleBinding) {
			srb.Subjects = append(srb.Subjects, &v1alpha1.Subject{
				User: "user/sa/bar",
			})
		},
	}

	adPair := fixtures.CreateAthenzDomain(o)
	rolloutAndValidate(t, adPair, false)

	adPair = fixtures.CreateAthenzDomain(&fixtures.Override{})
	rolloutAndValidate(t, adPair, true)
	cleanup(t, adPair)
}

// 2.3 Add unrelated AD changes (not conformant to RBAC)
func TestUpdateUnrelatedADField(t *testing.T) {
	ogPair := fixtures.CreateAthenzDomain(&fixtures.Override{})
	rolloutAndValidate(t, ogPair, false)

	o := &fixtures.Override{
		ModifyAD: func(signedDomain *zms.SignedDomain) {
			signedDomain.KeyId = "col-env-1.2"
		},
	}

	// TODO, hacky?
	adPair := fixtures.CreateAthenzDomain(o)
	ogPair.AD = adPair.AD

	rolloutAndValidate(t, ogPair, true)
	cleanup(t, ogPair)
}

func checkDeletion(t *testing.T, configs []model.Config) {
	for _, config := range configs {
		c := framework.Global.IstioClientset.Get(config.Type, config.Name, config.Namespace)
		assert.Nil(t, c, "should be nil")
	}
}

// 2.4 Update role name and expect the old SR/SRB to be deleted
func TestUpdateRoleName(t *testing.T) {
	ogPair := fixtures.CreateAthenzDomain(&fixtures.Override{})
	rolloutAndValidate(t, ogPair, false)

	o := &fixtures.Override{
		ModifyAD: func(signedDomain *zms.SignedDomain) {
			signedDomain.Domain.Policies.Contents.Policies[0].Assertions[0].Role = "athenz.domain:role.client-reader-role"
			signedDomain.Domain.Roles[0].Name = "athenz.domain:role.client-reader-role"
		},
	}

	adPair := fixtures.CreateAthenzDomain(o)
	ogPair.AD = adPair.AD
	rolloutAndValidate(t, ogPair, true)
	checkDeletion(t, ogPair.ModelConfigs)
	cleanup(t, adPair)
}

// 2.5 Test updates with multiple namespaces / AD
func TestMultipleAD(t *testing.T) {

}
