package main

import (
	"fmt"
	"testing"
)

// https://epsfs.hindustantimes.com/LH/2024/02/11/RANxHAZB/5_01/d6b6d096_01.jpg
// https://epsfs.hindustantimes.com/LH/2024/02/11/RANxHAZB/5_01/d6b6d096d54d2f5c_01_hr.jpg
// https://epsfs.hindustantimes.com/LH/2024/02/11/RANxHAZB/5_01/d6b6d096_01_mr.jpg

// https://epsfs.hindustantimes.com/LH/2024/02/11/RANxPLMU/5_16/d60c92d017a9104b_16_hr.jpg
// https://epsfs.hindustantimes.com/LH/2024/02/11/RANxPLMU/5_16/d60c92d0_16_mr.jpg
func TestCreatePdf(t *testing.T) {
	// create filenames {}
	filenames := []string{}
	for i := 0; i < 16; i++ {
		filename := fmt.Sprintf("page_%d.jpg", i)
		filenames = append(filenames, filename)
	}
	err := CreatePdf(filenames, "output16.pdf")
	if err != nil {
		t.Errorf("CreatePdf() failed, expected nil, got %v", err)
	}
}
