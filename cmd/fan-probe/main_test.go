//go:build windows

package main

import "testing"

func TestVendorPaths(t *testing.T) {
	r := &report{}
	for _, n := range []string{"WSAT_TraceRecord", "AsusAtkWmi_WMNB", "LENOVO_FAN_METHOD", "AWCCWmiMethodFunction", "hpqBIntM", "AcerGamingFunction"} {
		r.WMI.Vendor = append(r.WMI.Vendor, wmiClass{Name: n})
	}
	found := r.vendorFound()
	want := map[string]string{"Lenovo Legion": "LENOVO_FAN_METHOD", "Dell Alienware/G 시리즈": "AWCCWmiMethodFunction", "HP": "hpqBIntM", "Acer": "AcerGamingFunction"}
	if len(found) != len(want) {
		t.Fatalf("found %v", found)
	}
	for k, v := range want {
		if len(found[k]) != 1 || found[k][0] != v {
			t.Errorf("%s: %v", k, found[k])
		}
	}
}

func TestServiceWords(t *testing.T) {
	for _, s := range []string{`"C:\Program Files\Docker\x.exe"`, "Google Updater", "pacer.sys", "IpFilterDriver", "MSiSCSI", "msisadrv"} {
		if vendorWords.MatchString(s) {
			t.Errorf("%q matched", s)
		}
	}
	for _, s := range []string{"ROG Live Service", "LenovoVantageService", "AWCCService", "HP Omen", "MSI Center", "Acer Quick Access"} {
		if !vendorWords.MatchString(s) {
			t.Errorf("%q not matched", s)
		}
	}
}
