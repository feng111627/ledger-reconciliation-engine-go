package recon

import (
	"encoding/json"
	"os"
)

// readRawStore 仅供测试：绕过文件锁直接统计结论条数。
func readRawStore(path string) (storeData, error) {
	var data storeData
	b, err := os.ReadFile(path)
	if err != nil {
		return data, err
	}
	if len(b) == 0 {
		return data, nil
	}
	err = json.Unmarshal(b, &data)
	return data, err
}
