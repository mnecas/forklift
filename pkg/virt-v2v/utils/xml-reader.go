package utils

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

type InspectionOS struct {
	Name   string `xml:"name"`
	Distro string `xml:"distro"`
	Osinfo string `xml:"osinfo"`
	Arch   string `xml:"arch"`
}

type InspectionV2V struct {
	XMLName xml.Name     `xml:"v2v"`
	OS      InspectionOS `xml:"operatingsystem"`
}

type inspectionV2VXML struct {
	XMLName xml.Name     `xml:"v2v"`
	OS      InspectionOS `xml:"operatingsystem"`
}

type inspectionVirtInspectorXML struct {
	XMLName          xml.Name       `xml:"operatingsystems"`
	OperatingSystems []InspectionOS `xml:"operatingsystem"`
}

func GetInspectionV2vFromFile(xmlFilePath string) (*InspectionV2V, error) {
	xmlData, err := os.ReadFile(xmlFilePath)
	if err != nil {
		fmt.Printf("Error read XML: %v\n", err)
		return nil, err
	}

	return GetInspectionV2v(xmlData)
}

func GetInspectionV2v(xmlData []byte) (*InspectionV2V, error) {
	var v2v inspectionV2VXML
	if err := xml.Unmarshal(xmlData, &v2v); err == nil && v2v.XMLName.Local == "v2v" {
		return &InspectionV2V{
			OS: v2v.OS,
		}, nil
	}

	var virtInspector inspectionVirtInspectorXML
	err := xml.Unmarshal(xmlData, &virtInspector)
	if err != nil {
		return nil, fmt.Errorf("Error unmarshalling XML: %v\n", err)
	}

	var os InspectionOS
	if len(virtInspector.OperatingSystems) > 0 {
		os = virtInspector.OperatingSystems[0]
	}
	return &InspectionV2V{
		OS: os,
	}, nil
}

func WriteInspectionV2vToFile(xmlFilePath string, inspection *InspectionV2V) error {
	if inspection == nil {
		return fmt.Errorf("inspection is nil")
	}

	xmlData, err := xml.MarshalIndent(inspection, "", "  ")
	if err != nil {
		return fmt.Errorf("Error marshalling XML: %v\n", err)
	}

	xmlData = append([]byte(xml.Header), xmlData...)
	return os.WriteFile(xmlFilePath, xmlData, 0644)
}

func (os InspectionOS) IsWindows() bool {
	return strings.Contains(strings.ToLower(os.Osinfo), "win")
}
