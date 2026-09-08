package linuxapi

// stripe は画面を横に切った1本の帯。
type stripe struct {
	Y      int
	Height int
}

// imageStripes は画面を GetImage で読むための帯に分ける。
//
// X の応答は1回あたりの上限に収まる必要があり、4K や複数モニタの
// 仮想画面は必ず超える。超えたまま要求すると応答が壊れるか、
// サーバが要求ごと拒む。
//
// maxReplyBytes は1回の応答で受け取れるピクセルのバイト数。
// 1行すら入らないときは高さ1の帯を返す（読めるかは呼び出し側が確かめる）。
func imageStripes(width, height, maxReplyBytes, bytesPerPixel int) []stripe {
	if width <= 0 || height <= 0 || bytesPerPixel <= 0 {
		return nil
	}

	rowBytes := width * bytesPerPixel
	rowsPerStripe := 1
	if rowBytes > 0 && maxReplyBytes >= rowBytes {
		rowsPerStripe = maxReplyBytes / rowBytes
	}
	if rowsPerStripe > height {
		rowsPerStripe = height
	}

	stripes := make([]stripe, 0, (height+rowsPerStripe-1)/rowsPerStripe)
	for y := 0; y < height; y += rowsPerStripe {
		stripes = append(stripes, stripe{Y: y, Height: min(height-y, rowsPerStripe)})
	}
	return stripes
}
