/*-
 * Copyright (c) 2016-2020, F5 Networks, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package as3

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/F5Networks/k8s-bigip-ctlr/pkg/writer"

	. "github.com/F5Networks/k8s-bigip-ctlr/pkg/resource"
	log "github.com/F5Networks/k8s-bigip-ctlr/pkg/vlogger"
)

const (
	svcTenantLabel      = "cis.f5.com/as3-tenant="
	svcAppLabel         = "cis.f5.com/as3-app="
	svcPoolLabel        = "cis.f5.com/as3-pool="
	as3SupportedVersion = 3.18
	//Update as3Version,defaultAS3Version,defaultAS3Build while updating AS3 validation schema
	as3Version           = 3.21
	defaultAS3Version    = "3.21.0"
	defaultAS3Build      = "4"
	as3tenant            = "Tenant"
	as3class             = "class"
	as3SharedApplication = "Shared"
	as3application       = "Application"
	as3shared            = "shared"
	as3template          = "template"
	//as3SchemaLatestURL   = "https://raw.githubusercontent.com/F5Networks/f5-appsvcs-extension/master/schema/latest/as3-schema.json"
	as3SchemaFileName = "as3-schema-3.21.0-4-cis.json"
)

var baseAS3Config = `{
	"$schema": "https://raw.githubusercontent.com/F5Networks/f5-appsvcs-extension/master/schema/%s/as3-schema-%s.json",
	"class": "AS3",
	"declaration": {
	  "class": "ADC",
	  "schemaVersion": "%s",
	  "id": "urn:uuid:85626792-9ee7-46bb-8fc8-4ba708cfdc1d",
	  "label": "CIS Declaration",
	  "remark": "Auto-generated by CIS",
	  "controls": {
		 "class": "Controls",
		 "userAgent": "CIS Configured AS3"
	  }
	}
  }
  `

// AS3Config consists of all the AS3 related configurations
type AS3Config struct {
	resourceConfig        as3ADC
	configmaps            []*AS3ConfigMap
	overrideConfigmapData string
	unifiedDeclaration    as3Declaration
}

// ActiveAS3ConfigMap user defined ConfigMap for global availability.
type AS3ConfigMap struct {
	Name      string   // AS3 specific ConfigMap name
	Namespace string   // AS3 specific ConfigMap namespace
	config    as3ADC   // if AS3 Name is present, populate this with AS3 template data.
	endPoints []Member // Endpoints of all the pools in the configmap
}

// AS3Manager holds all the AS3 orchestration specific config
type AS3Manager struct {
	as3Validation             bool
	sslInsecure               bool
	enableTLS                 string
	tls13CipherGroupReference string
	ciphers                   string
	// Active User Defined ConfigMap details
	as3ActiveConfig AS3Config
	As3SchemaLatest string
	// Override existing as3 declaration with this configmap
	OverriderCfgMapName string
	// Path of schemas reside locally
	SchemaLocalPath string
	// POSTs configuration to BIG-IP using AS3
	PostManager *PostManager
	// To put list of tenants in BIG-IP REST call URL that are in AS3 declaration
	FilterTenants    bool
	DefaultPartition string
	ReqChan          chan MessageRequest
	RspChan          chan interface{}
	userAgent        string
	l2l3Agent        L2L3Agent
	ResourceRequest
	ResourceResponse
	as3Version                string
	as3Release                string
	unprocessableEntityStatus bool
}

// Struct to allow NewManager to receive all or only specific parameters.
type Params struct {
	// Package local for unit testing only
	SchemaLocal               string
	AS3Validation             bool
	SSLInsecure               bool
	EnableTLS                 string
	TLS13CipherGroupReference string
	Ciphers                   string
	//Agent                     string
	OverriderCfgMapName string
	SchemaLocalPath     string
	FilterTenants       bool
	BIGIPUsername       string
	BIGIPPassword       string
	BIGIPURL            string
	TrustedCerts        string
	AS3PostDelay        int
	ConfigWriter        writer.Writer
	EventChan           chan interface{}
	//Log the AS3 response body in Controller logs
	LogResponse               bool
	RspChan                   chan interface{}
	UserAgent                 string
	As3Version                string
	As3Release                string
	unprocessableEntityStatus bool
}

// Create and return a new app manager that meets the Manager interface
func NewAS3Manager(params *Params) *AS3Manager {
	as3Manager := AS3Manager{
		as3Validation:             params.AS3Validation,
		sslInsecure:               params.SSLInsecure,
		enableTLS:                 params.EnableTLS,
		tls13CipherGroupReference: params.TLS13CipherGroupReference,
		ciphers:                   params.Ciphers,
		SchemaLocalPath:           params.SchemaLocal,
		FilterTenants:             params.FilterTenants,
		RspChan:                   params.RspChan,
		userAgent:                 params.UserAgent,
		as3Version:                params.As3Version,
		as3Release:                params.As3Release,
		OverriderCfgMapName:       params.OverriderCfgMapName,
		l2l3Agent: L2L3Agent{eventChan: params.EventChan,
			configWriter: params.ConfigWriter},
		PostManager: NewPostManager(PostParams{
			BIGIPUsername: params.BIGIPUsername,
			BIGIPPassword: params.BIGIPPassword,
			BIGIPURL:      params.BIGIPURL,
			TrustedCerts:  params.TrustedCerts,
			SSLInsecure:   params.SSLInsecure,
			AS3PostDelay:  params.AS3PostDelay,
			LogResponse:   params.LogResponse}),
	}

	as3Manager.fetchAS3Schema()

	return &as3Manager
}

func (am *AS3Manager) postAS3Declaration(rsReq ResourceRequest) (bool, string) {

	am.ResourceRequest = rsReq

	//as3Config := am.as3ActiveConfig
	as3Config := &AS3Config{}

	// Process Route or Ingress
	as3Config.resourceConfig = am.prepareAS3ResourceConfig()

	// Process all Configmaps (including overrideAS3)
	as3Config.configmaps, as3Config.overrideConfigmapData = am.prepareResourceAS3ConfigMaps()

	return am.postAS3Config(*as3Config)
}

func (am *AS3Manager) postAS3Config(tempAS3Config AS3Config) (bool, string) {
	unifiedDecl := am.getUnifiedDeclaration(&tempAS3Config)
	if unifiedDecl == "" {
		return true, ""
	}
	if DeepEqualJSON(am.as3ActiveConfig.unifiedDeclaration, unifiedDecl) {
		return !am.unprocessableEntityStatus, ""
	}

	if am.as3Validation == true {
		if ok := am.validateAS3Template(string(unifiedDecl)); !ok {
			return true, ""
		}
	}

	log.Debugf("[AS3] Posting AS3 Declaration")

	am.as3ActiveConfig.updateConfig(tempAS3Config)

	var tenants []string = nil

	if am.FilterTenants {
		tenants = getTenants(unifiedDecl, true)
	}

	return am.PostManager.postConfig(string(unifiedDecl), tenants)
}

func (cfg *AS3Config) updateConfig(newAS3Cfg AS3Config) {
	cfg.resourceConfig = newAS3Cfg.resourceConfig
	cfg.unifiedDeclaration = newAS3Cfg.unifiedDeclaration
	cfg.configmaps = newAS3Cfg.configmaps
	cfg.overrideConfigmapData = newAS3Cfg.overrideConfigmapData
}

func (am *AS3Manager) getUnifiedDeclaration(cfg *AS3Config) as3Declaration {
	// Need to process Routes
	var as3Obj map[string]interface{}

	baseAS3ConfigTemplate := fmt.Sprintf(baseAS3Config, am.as3Version, am.as3Release, am.as3Version)
	_ = json.Unmarshal([]byte(baseAS3ConfigTemplate), &as3Obj)
	adc, _ := as3Obj["declaration"].(map[string]interface{})

	for tenantName, tenant := range cfg.resourceConfig {
		adc[tenantName] = tenant
	}

	for _, cm := range cfg.configmaps {
		for tenantName, tenant := range cm.config {
			adc[tenantName] = tenant
		}
	}

	for _, tnt := range am.getDeletedTenants(adc) {
		// This config deletes the partition in BIG-IP
		adc[tnt] = as3Tenant{
			"class": "Tenant",
		}
	}

	unifiedDecl, err := json.Marshal(as3Obj)
	if err != nil {
		log.Debugf("[AS3] Unified declaration: %v\n", err)
	}

	if cfg.overrideConfigmapData == "" {
		cfg.unifiedDeclaration = as3Declaration(unifiedDecl)
		return as3Declaration(unifiedDecl)
	}

	overriddenUnifiedDecl := ValidateAndOverrideAS3JsonData(
		cfg.overrideConfigmapData,
		string(unifiedDecl),
	)
	if overriddenUnifiedDecl == "" {
		log.Debug("[AS3] Failed to override AS3 Declaration")
		cfg.unifiedDeclaration = as3Declaration(unifiedDecl)
		return as3Declaration(unifiedDecl)
	}
	cfg.unifiedDeclaration = as3Declaration(overriddenUnifiedDecl)
	return as3Declaration(overriddenUnifiedDecl)
}

// Function to prepare empty AS3 declaration
func (am *AS3Manager) getEmptyAs3Declaration(partition string) as3Declaration {
	var as3Config map[string]interface{}
	baseAS3ConfigEmpty := fmt.Sprintf(baseAS3Config, am.as3Version, am.as3Release, am.as3Version)
	_ = json.Unmarshal([]byte(baseAS3ConfigEmpty), &as3Config)
	decl := as3Config["declaration"].(map[string]interface{})

	controlObj := make(as3Control)
	controlObj.initDefault(am.userAgent)
	decl["controls"] = controlObj
	if partition != "" {

		decl[partition] = map[string]string{"class": "Tenant"}
	}
	data, _ := json.Marshal(as3Config)
	return as3Declaration(data)
}

// Function to prepare tenantobjects
func (am *AS3Manager) getTenantObjects(partitions []string) string {
	var as3Config map[string]interface{}
	baseAS3ConfigEmpty := fmt.Sprintf(baseAS3Config, am.as3Version, am.as3Release, am.as3Version)
	_ = json.Unmarshal([]byte(baseAS3ConfigEmpty), &as3Config)
	decl := as3Config["declaration"].(map[string]interface{})
	for _, partition := range partitions {

		decl[partition] = map[string]string{"class": "Tenant"}
	}
	data, _ := json.Marshal(as3Config)
	return string(data)
}

func (am *AS3Manager) getDeletedTenants(curTenantMap map[string]interface{}) []string {
	prevTenants := getTenants(am.as3ActiveConfig.unifiedDeclaration, false)
	var deletedTenants []string

	for _, tnt := range prevTenants {
		if _, found := curTenantMap[tnt]; !found {
			deletedTenants = append(deletedTenants, tnt)
		}
	}
	return deletedTenants
}

// Method to delete any AS3 partition
func (am *AS3Manager) DeleteAS3Partition(partition string) (bool, string) {
	emptyAS3Declaration := am.getEmptyAs3Declaration(partition)
	return am.PostManager.postConfig(string(emptyAS3Declaration), nil)
}

// fetchAS3Schema ...
func (am *AS3Manager) fetchAS3Schema() {
	log.Debugf("[AS3] Validating AS3 schema with  %v", as3SchemaFileName)
	am.As3SchemaLatest = am.SchemaLocalPath + as3SchemaFileName
	return
}

// configDeployer blocks on ReqChan
// whenever gets unblocked posts active configuration to BIG-IP
func (am *AS3Manager) ConfigDeployer() {
	// For the very first post after starting controller, need not wait to post
	firstPost := true
	am.unprocessableEntityStatus = false
	for msgReq := range am.ReqChan {
		if !firstPost && am.PostManager.AS3PostDelay != 0 {
			// Time (in seconds) that CIS waits to post the AS3 declaration to BIG-IP.
			log.Debugf("[AS3] Delaying post to BIG-IP for %v seconds", am.PostManager.AS3PostDelay)
			_ = <-time.After(time.Duration(am.PostManager.AS3PostDelay) * time.Second)
		}

		// After postDelay expires pick up latest declaration, if available
		select {
		case msgReq = <-am.ReqChan:
		case <-time.After(1 * time.Microsecond):
		}

		posted, event := am.postAS3Declaration(msgReq.ResourceRequest)
		// To handle general errors
		for !posted {
			am.unprocessableEntityStatus = true
			timeout := getTimeDurationForErrorResponse(event)
			log.Debugf("[AS3] Error handling for event %v", event)
			posted, event = am.postOnEventOrTimeout(timeout)
		}
		firstPost = false
		if event == responseStatusOk {
			am.unprocessableEntityStatus = false
			log.Debugf("[AS3] Preparing response message to response handler")
			am.SendARPEntries()
			am.SendAgentResponse()
			log.Debugf("[AS3] Sent response message to response handler")
		}
	}
}

// Helper method used by configDeployer to handle error responses received from BIG-IP
func (am *AS3Manager) postOnEventOrTimeout(timeout time.Duration) (bool, string) {
	select {
	case msgReq := <-am.ReqChan:
		return am.postAS3Declaration(msgReq.ResourceRequest)
	case <-time.After(timeout):
		var tenants []string = nil
		if am.FilterTenants {
			tenants = getTenants(am.as3ActiveConfig.unifiedDeclaration, true)
		}
		unifiedDeclaration := string(am.as3ActiveConfig.unifiedDeclaration)
		return am.PostManager.postConfig(unifiedDeclaration, tenants)
	}
}

// Post ARP entries over response channel
func (am *AS3Manager) SendAgentResponse() {
	agRsp := am.ResourceResponse
	agRsp.IsResponseSuccessful = true
	am.postAgentResponse(MessageResponse{ResourceResponse: agRsp})
}

// Method implements posting MessageResponse on Agent Response Channel
func (am *AS3Manager) postAgentResponse(msgRsp MessageResponse) {
	select {
	case am.RspChan <- msgRsp:
	case <-am.RspChan:
		am.RspChan <- msgRsp
	}
}

// Method to verify if App Services are installed or CIS as3 version is
// compatible with BIG-IP, it will return with error if any one of the
// requirements are not met
func (am *AS3Manager) IsBigIPAppServicesAvailable() error {
	version, build, err := am.PostManager.GetBigipAS3Version()
	am.as3Version = version
	as3Build := build
	am.as3Release = am.as3Version + "-" + as3Build
	if err != nil {
		log.Errorf("[AS3] %v ", err)
		return err
	}
	versionstr := version[:strings.LastIndex(version, ".")]
	bigIPAS3Version, err := strconv.ParseFloat(versionstr, 64)
	if err != nil {
		log.Errorf("[AS3] Error while converting AS3 version to float")
		return err
	}
	if bigIPAS3Version >= as3SupportedVersion && bigIPAS3Version <= as3Version {
		log.Debugf("[AS3] BIGIP is serving with AS3 version: %v", version)
		return nil
	}

	if bigIPAS3Version > as3Version {
		am.as3Version = defaultAS3Version
		as3Build := defaultAS3Build
		am.as3Release = am.as3Version + "-" + as3Build
		log.Debugf("[AS3] BIGIP is serving with AS3 version: %v", bigIPAS3Version)
		return nil
	}

	return fmt.Errorf("CIS versions >= 2.0 are compatible with AS3 versions >= %v. "+
		"Upgrade AS3 version in BIGIP from %v to %v or above.", as3SupportedVersion,
		bigIPAS3Version, as3SupportedVersion)
}