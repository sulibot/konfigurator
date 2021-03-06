package model

import (
	"github.com/ghodss/yaml"

	"io/ioutil"

	"bytes"
	"encoding/json"
	"os"

	"fmt"

	"regexp"

	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/Wikia/konfigurator/helpers"
	v1 "k8s.io/client-go/pkg/api/v1"
	v1beta1 "k8s.io/client-go/pkg/apis/extensions/v1beta1"
)

var (
	timeStampRegex        = regexp.MustCompile(`\s+creationTimestamp: null`)
	emptyStructRegex      = regexp.MustCompile(`\s+(?:status|selector|strategy): {}`)
	yamlDocumentSeparator = []byte("---\n")
)

func splitYamlDocument(contents []byte) [][]byte {
	return bytes.Split(contents, yamlDocumentSeparator)
}

func ReadSecrets(filePath string) (*v1.Secret, [][]byte, error) {
	contents, err := ioutil.ReadFile(filePath)

	if err != nil {
		return nil, nil, err
	}

	secret := v1.Secret{}
	idx := 0
	var document []byte
	documents := splitYamlDocument(contents)

	for idx, document = range documents {
		err = yaml.Unmarshal(document, &secret)

		if err != nil {
			log.WithError(err).Warn("Error parsing YAML document")
			continue
		}

		if secret.Kind == "Secret" {
			break
		}
	}

	if secret.Kind != "Secret" {
		return nil, nil, fmt.Errorf("Could not unmarshall Secrets")
	}

	return &secret, append(documents[0:idx], documents[idx+1:]...), nil
}

func WriteSecrets(secret *v1.Secret, leftOver [][]byte, filePath string) error {
	return writeK8sYaml(secret, leftOver, filePath)
}

func ReadConfigMap(filePath string) (*v1.ConfigMap, [][]byte, error) {
	contents, err := ioutil.ReadFile(filePath)

	if err != nil {
		return nil, nil, err
	}

	configMap := v1.ConfigMap{}
	idx := 0
	var document []byte
	documents := splitYamlDocument(contents)

	for idx, document = range documents {
		err = yaml.Unmarshal(document, &configMap)

		if err != nil {
			log.WithError(err).Warn("Error parsing YAML document")
			continue
		}

		if configMap.Kind == "ConfigMap" {
			break
		}
	}

	if configMap.Kind != "ConfigMap" {
		return nil, nil, fmt.Errorf("Could not unmarshall ConfigMap")
	}

	return &configMap, append(documents[0:idx], documents[idx+1:]...), nil
}

func WriteConfigMap(configMap *v1.ConfigMap, leftOver [][]byte, filePath string) error {
	return writeK8sYaml(configMap, leftOver, filePath)
}

func ReadDeployment(filePath string) (*v1beta1.Deployment, [][]byte, error) {
	contents, err := ioutil.ReadFile(filePath)

	if err != nil {
		return nil, nil, err
	}

	deployment := v1beta1.Deployment{}
	idx := 0
	var document []byte
	documents := splitYamlDocument(contents)

	for idx, document = range splitYamlDocument(contents) {
		err = yaml.Unmarshal(document, &deployment)

		if err != nil {
			log.WithError(err).Warn("Error parsing YAML document")
			continue
		}

		if deployment.Kind == "Deployment" {
			break
		}
	}

	if deployment.Kind != "Deployment" {
		return nil, nil, fmt.Errorf("Could not unmarshall Deployment")
	}

	return &deployment, append(documents[0:idx], documents[idx+1:]...), nil
}

func WriteDeployment(deployment *v1beta1.Deployment, leftOver [][]byte, filePath string) error {
	return writeK8sYaml(deployment, leftOver, filePath)
}

func marshalK8sEntity(obj interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	output, err := yaml.JSONToYAML(jsonData)
	if err != nil {
		return nil, err
	}

	output = timeStampRegex.ReplaceAll(output, []byte(""))
	output = emptyStructRegex.ReplaceAll(output, []byte(""))

	return output, nil
}

func writeK8sYaml(obj interface{}, leftOver [][]byte, filePath string) error {
	secretFile, err := os.Create(filePath)
	if err != nil {
		return err
	}

	defer secretFile.Close()

	output, err := marshalK8sEntity(obj)

	if err != nil {
		return err
	}

	leftOver = append(leftOver, output)

	_, err = secretFile.Write(bytes.Join(leftOver, yamlDocumentSeparator))

	if err != nil {
		return err
	}

	return nil
}

func DiffDeploymets(deployment1 *v1beta1.Deployment, deployment2 *v1beta1.Deployment) error {
	deployYaml1, err := marshalK8sEntity(deployment1)

	if err != nil {
		return err
	}

	deployYaml2, err := marshalK8sEntity(deployment2)

	if err != nil {
		return err
	}

	helpers.RenderDiff(os.Stdout, string(deployYaml1), string(deployYaml2))

	return nil
}

func UpdateDeployment(deployment *v1beta1.Deployment, configMap *v1.ConfigMap, secret *v1.Secret, containerName string, variables []VariableDef, overwriteEnv bool) error {
	var dstContainer *v1.Container

	for idx, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == containerName {
			dstContainer = &deployment.Spec.Template.Spec.Containers[idx]
			break
		}
	}

	if dstContainer == nil {
		return fmt.Errorf("Could not find container '%s' in deployment", containerName)
	}

	if overwriteEnv {
		dstContainer.Env = []v1.EnvVar{}
	}

	for _, variable := range variables {
		var envVarSource *v1.EnvVarSource

		switch variable.Type {
		case CONFIGMAP:
			envVarSource = &v1.EnvVarSource{
				ConfigMapKeyRef: &v1.ConfigMapKeySelector{
					Key:                  strings.ToLower(variable.Name),
					LocalObjectReference: v1.LocalObjectReference{Name: configMap.Name},
				},
			}
		case SECRET:
			envVarSource = &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					Key:                  strings.ToLower(variable.Name),
					LocalObjectReference: v1.LocalObjectReference{Name: secret.Name},
				},
			}
		case REFERENCE:
			envVarSource = &v1.EnvVarSource{
				FieldRef: &v1.ObjectFieldSelector{
					FieldPath: variable.Value.(string),
				},
			}
		}

		for _, envs := range dstContainer.Env {
			if envs.Name == strings.ToUpper(variable.Name) {
				envs.Value = ""
				envs.ValueFrom = envVarSource
				envVarSource = nil
				break
			}
		}

		if envVarSource != nil {
			dstContainer.Env = append(dstContainer.Env, v1.EnvVar{Name: strings.ToUpper(variable.Name), ValueFrom: envVarSource})
			envVarSource = nil
		}
	}

	return nil
}
