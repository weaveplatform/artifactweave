package acceptance

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func (w *world) registerDescriptor(sc *godog.ScenarioContext) {
	sc.Step(`^I PATCH descriptor file "([^"]*)" as admin$`, w.patchDescriptorFile)
	sc.Step(`^I apply descriptor file "([^"]*)" to the server store$`, w.applyDescriptorFile)
	sc.Step(`^the XML field "([^"]*)" equals "([^"]*)"$`, w.xmlFieldEquals)
	w.registerMail(sc)
}

func (w *world) patchDescriptorFile(name string) error {
	body, err := os.ReadFile(filepath.Join(w.dir, name))
	if err != nil {
		return err
	}
	return w.patchYAMLAsAdmin("/artifactory/api/system/configuration", &godog.DocString{Content: string(body)})
}

func (w *world) applyDescriptorFile(name string) error {
	cmd := exec.Command(binary, "config", "apply", filepath.Join(w.dir, name), "--data-dir", w.dataDir)
	cmd.Env = append(os.Environ(), "ARTIFACTWEAVE_MASTER_KEY="+testMasterKey)
	if coverDir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
	}
	body, err := cmd.CombinedOutput()
	w.body = body
	if err != nil {
		return fmt.Errorf("config apply: %w: %s", err, body)
	}
	return nil
}

type descriptorXMLNode struct {
	Name     xml.Name
	Text     string              `xml:",chardata"`
	Children []descriptorXMLNode `xml:",any"`
}

func (n *descriptorXMLNode) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	type node descriptorXMLNode
	var value node
	if err := decoder.DecodeElement(&value, &start); err != nil {
		return err
	}
	*n = descriptorXMLNode(value)
	n.Name = start.Name
	return nil
}

func (w *world) xmlFieldEquals(path, want string) error {
	var root descriptorXMLNode
	if err := xml.Unmarshal(w.body, &root); err != nil {
		return fmt.Errorf("response is not XML: %w", err)
	}
	nodes := []descriptorXMLNode{root}
	for _, part := range strings.Split(path, "/") {
		name, selector, filtered := strings.Cut(part, "[")
		var next []descriptorXMLNode
		for _, node := range nodes {
			for _, child := range node.Children {
				if child.Name.Local == name {
					if !filtered || child.matches(strings.TrimSuffix(selector, "]")) {
						next = append(next, child)
					}
				}
			}
		}
		nodes = next
	}
	if len(nodes) != 1 {
		return fmt.Errorf("XML field %s has %d matches", path, len(nodes))
	}
	if nodes[0].Text != want {
		return fmt.Errorf("XML field %s = %q, want %q", path, nodes[0].Text, want)
	}
	return nil
}

func (n descriptorXMLNode) matches(selector string) bool {
	key, value, _ := strings.Cut(selector, "=")
	for _, child := range n.Children {
		if child.Name.Local == key && child.Text == value {
			return true
		}
	}
	return false
}
