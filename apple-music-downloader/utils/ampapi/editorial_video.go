package ampapi

import "strings"

type EditorialVideo struct {
	MotionTall struct {
		Video string `json:"video"`
	} `json:"motionTallVideo3x4"`
	MotionSquare struct {
		Video string `json:"video"`
	} `json:"motionSquareVideo1x1"`
	MotionDetailTall struct {
		Video string `json:"video"`
	} `json:"motionDetailTall"`
	MotionDetailSquare struct {
		Video string `json:"video"`
	} `json:"motionDetailSquare"`
}

func (v EditorialVideo) SquareURL() string {
	if s := strings.TrimSpace(v.MotionDetailSquare.Video); s != "" {
		return s
	}
	return strings.TrimSpace(v.MotionSquare.Video)
}

func (v EditorialVideo) TallURL() string {
	if s := strings.TrimSpace(v.MotionDetailTall.Video); s != "" {
		return s
	}
	return strings.TrimSpace(v.MotionTall.Video)
}
