package onedriveclient

import (
	"encoding/json"
	"fmt"
	"github.com/koofr/go-httpclient"
	"github.com/koofr/go-ioutils"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type OneDrive struct {
	ApiClient     *httpclient.HTTPClient
	ContentClient *httpclient.HTTPClient
	Auth          *OneDriveAuth
}

type OneDriveAuth struct {
	ClientId     string
	ClientSecret string
	RedirectUri  string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func (d *OneDriveAuth) ValidToken() (token string, err error) {
	if time.Now().Unix() > d.ExpiresAt.Unix() {
		data := url.Values{}
		data.Set("grant_type", "refresh_token")
		data.Set("client_id", d.ClientId)
		data.Set("client_secret", d.ClientSecret)
		data.Set("redirect_uri", d.RedirectUri)
		data.Set("refresh_token", d.RefreshToken)

		var resp *http.Response

		resp, err = http.PostForm("https://login.live.com/oauth20_token.srf", data)
		if err != nil {
			return
		}

		if resp.StatusCode != 200 {
			err = fmt.Errorf("Token refresh failed %d: %s", resp.StatusCode, resp.Status)
			return
		}

		var buf []byte
		if buf, err = ioutil.ReadAll(resp.Body); err != nil {
			return
		}

		var respVal RefreshResp
		if err = json.Unmarshal(buf, &respVal); err != nil {
			return
		}

		d.AccessToken = respVal.AccessToken
		d.ExpiresAt = time.Now().Add(time.Duration(respVal.ExpiresIn) * time.Second)
	}
	token = d.AccessToken
	return
}

func NewOneDriveClient(auth OneDriveAuth) *OneDrive {
	apiBaseUrl, _ := url.Parse("https://apis.live.net/v5.0")
	apiHttpClient := httpclient.New()
	apiHttpClient.BaseURL = apiBaseUrl
	return &OneDrive{apiHttpClient, httpclient.New(), &auth}
}

func (d *OneDrive) AuthenticationHeader() (hs http.Header, err error) {
	token, err := d.Auth.ValidToken()
	if err != nil {
		return
	}

	hs = make(http.Header)
	hs.Set("Authorization", "Bearer "+token)

	return
}

func (d *OneDrive) NodeInfo(id string) (info NodeInfo, err error) {
	header, err := d.AuthenticationHeader()
	if err != nil {
		return
	}

	req := &httpclient.RequestData{
		Method:         "GET",
		Path:           "/" + id,
		Headers:        header,
		ExpectedStatus: []int{200},
		RespEncoding:   httpclient.EncodingJSON,
		RespValue:      &info,
	}
	_, err = d.ApiClient.Request(req)
	if err != nil {
		return
	}
	return
}

func (d *OneDrive) RootInfo() (info NodeInfo, err error) {
	info, err = d.NodeInfo("me/skydrive")
	return
}

func (d *OneDrive) NodeFiles(id string) (files []NodeInfo, err error) {
	header, err := d.AuthenticationHeader()
	if err != nil {
		return
	}

	var resp NodeFiles
	req := &httpclient.RequestData{
		Method:         "GET",
		Path:           "/" + id + "/files",
		Headers:        header,
		ExpectedStatus: []int{200},
		RespEncoding:   httpclient.EncodingJSON,
		RespValue:      &resp,
	}
	_, err = d.ApiClient.Request(req)
	if err != nil {
		return
	}

	files = resp.Data
	return
}

func (d *OneDrive) Download(id string, span *ioutils.FileSpan) (info NodeInfo, content io.ReadCloser, err error) {
	info, err = d.NodeInfo(id)
	if err != nil {
		return
	}

	url := info.Source
	if url == "" {
		err = fmt.Errorf("Cannot download %s", id)
		return
	}

	req := httpclient.RequestData{
		Method:         "GET",
		FullURL:        url,
		ExpectedStatus: []int{http.StatusOK, http.StatusPartialContent},
	}

	if span != nil {
		req.Headers = make(http.Header)
		req.Headers.Set("Range", fmt.Sprintf("bytes=%d-%d", span.Start, span.End))
	}

	res, err := d.ContentClient.Request(&req)
	if err != nil {
		return
	}

	info.Size = res.ContentLength

	content = res.Body
	return
}

func (d *OneDrive) Upload(dirId string, name string, content io.Reader) (err error) {
	_, err = d.UploadOverwrite(dirId, name, true, content)

	return
}

func (d *OneDrive) UploadOverwrite(dirId string, name string, overwrite bool, content io.Reader) (newName string, err error) {
	header, err := d.AuthenticationHeader()
	if err != nil {
		return
	}

	params := url.Values{}

	if overwrite {
		params.Set("overwrite", "true")
	} else {
		params.Set("overwrite", "ChooseNewName")
	}

	resp := &struct {
		Name string
	}{}

	req := httpclient.RequestData{
		Method:         "PUT",
		Path:           "/" + dirId + "/files/" + name,
		Params:         params,
		Headers:        header,
		ReqReader:      content,
		ExpectedStatus: []int{200, 201},
		RespValue:      resp,
		RespEncoding:   httpclient.EncodingJSON,
	}

	_, err = d.ApiClient.Request(&req)
	if err != nil {
		return
	}

	newName = resp.Name

	return
}

func (d *OneDrive) ResolvePath(pth string) (id string, err error) {
	root, err := d.RootInfo()
	if err != nil {
		return
	}
	id = root.Id

loopParts:
	for _, part := range pathParts(pth) {
		var files []NodeInfo
		files, err = d.NodeFiles(id)
		if err != nil {
			return
		}
		name := strings.ToLower(part)
		for _, file := range files {
			if strings.ToLower(file.Name) == name {
				id = file.Id
				continue loopParts
			}
		}
		return "", fmt.Errorf("Not found %s", part)
	}
	return
}

func pathParts(pth string) []string {
	pth = path.Clean("/" + pth)
	parts := make([]string, 0)
	for pth != "/" {
		var name string
		pth, name = path.Split(pth)
		pth = path.Clean(pth)
		parts = append(parts, name)
	}

	//in-place reverse
	l := len(parts) - 1
	h := len(parts) / 2
	for i := 0; i < h; i++ {
		t := parts[i]
		ii := l - i
		parts[i] = parts[ii]
		parts[ii] = t
	}
	return parts
}
