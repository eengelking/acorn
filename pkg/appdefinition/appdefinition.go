package appdefinition

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"sort"

	cue_mod "github.com/acorn-io/acorn/cue.mod"
	v1 "github.com/acorn-io/acorn/pkg/apis/acorn.io/v1"
	"github.com/acorn-io/acorn/pkg/cue"
	"github.com/acorn-io/acorn/schema"
)

const (
	AcornCueFile       = "acorn.cue"
	ImageDataFile      = "images.json"
	BuildDataFile      = "build.json"
	BuildTransform     = "github.com/acorn-io/acorn/schema/v1/transform/build"
	NormalizeTransform = "github.com/acorn-io/acorn/schema/v1/transform/normalize"
	Schema             = "github.com/acorn-io/acorn/schema/v1"
	AppType            = "#App"
)

type AppDefinition struct {
	ctx        *cue.Context
	imageDatas []v1.ImagesData
}

func FromAppImage(appImage *v1.AppImage) (*AppDefinition, error) {
	appDef, err := NewAppDefinition([]byte(appImage.Acornfile))
	if err != nil {
		return nil, err
	}

	appDef = appDef.WithImageData(appImage.ImageData)
	return appDef.WithBuildParams(appImage.BuildParams)
}

func (a *AppDefinition) WithImageData(imageData v1.ImagesData) *AppDefinition {
	return &AppDefinition{
		ctx:        a.ctx,
		imageDatas: append(a.imageDatas, imageData),
	}
}

func NewAppDefinition(data []byte) (*AppDefinition, error) {
	files := []cue.File{
		{
			Name: AcornCueFile,
			Data: data,
		},
	}
	ctx := cue.NewContext().
		WithNestedFS("schema", schema.Files).
		WithNestedFS("cue.mod", cue_mod.Files)
	ctx = ctx.WithFiles(files...)
	err := ctx.Validate(Schema, AppType)
	return &AppDefinition{
		ctx: ctx,
	}, err
}

func assignAcornImage(originalImage string, build *v1.AcornBuild, image string) (string, *v1.AcornBuild) {
	if build == nil {
		build = &v1.AcornBuild{}
	}
	if build.OriginalImage == "" {
		build.OriginalImage = originalImage
	}
	return image, build
}

func assignImage(originalImage string, build *v1.Build, image string) (string, *v1.Build) {
	if build == nil {
		build = &v1.Build{}
	}
	if build.BaseImage == "" {
		build.BaseImage = originalImage
	}
	return image, build
}

func (a *AppDefinition) WithDeployParams(params map[string]interface{}) (*AppDefinition, error) {
	if len(params) == 0 {
		return a, nil
	}
	data, err := json.Marshal(map[string]interface{}{
		"params": map[string]interface{}{
			"deploy": params,
		},
	})
	if err != nil {
		return nil, err
	}
	return &AppDefinition{
		ctx:        a.ctx.WithFile("deploy.cue", data),
		imageDatas: a.imageDatas,
	}, nil
}

func (a *AppDefinition) WithBuildParams(params map[string]interface{}) (*AppDefinition, error) {
	if len(params) == 0 {
		return a, nil
	}
	data, err := json.Marshal(map[string]interface{}{
		"params": map[string]interface{}{
			"build": params,
		},
	})
	if err != nil {
		return nil, err
	}
	return &AppDefinition{
		ctx:        a.ctx.WithFile("build.cue", data),
		imageDatas: a.imageDatas,
	}, nil
}

func (a *AppDefinition) JSON() (string, error) {
	app, err := a.ctx.Value()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(app, "", "  ")
	return string(data), err
}

func (a *AppDefinition) AppSpec() (*v1.AppSpec, error) {
	app, err := a.ctx.Value()
	if err != nil {
		return nil, err
	}

	v, err := a.ctx.Encode(map[string]interface{}{
		"app": app,
	})
	if err != nil {
		return nil, err
	}

	v, err = a.ctx.TransformValue(v, NormalizeTransform)
	if err != nil {
		return nil, err
	}

	spec := &v1.AppSpec{}
	if err := a.ctx.Decode(v, spec); err != nil {
		return nil, err
	}

	for _, imageData := range a.imageDatas {
		for c, con := range imageData.Containers {
			if conSpec, ok := spec.Containers[c]; ok {
				conSpec.Image, conSpec.Build = assignImage(conSpec.Image, conSpec.Build, con.Image)
				spec.Containers[c] = conSpec
			}
			for s, con := range con.Sidecars {
				if conSpec, ok := spec.Containers[c].Sidecars[s]; ok {
					conSpec.Image, conSpec.Build = assignImage(conSpec.Image, conSpec.Build, con.Image)
					spec.Containers[c].Sidecars[s] = conSpec
				}
			}
		}
		for c, con := range imageData.Jobs {
			if conSpec, ok := spec.Jobs[c]; ok {
				conSpec.Image, conSpec.Build = assignImage(conSpec.Image, conSpec.Build, con.Image)
				spec.Jobs[c] = conSpec
			}
			for s, con := range con.Sidecars {
				if conSpec, ok := spec.Jobs[c].Sidecars[s]; ok {
					conSpec.Image, conSpec.Build = assignImage(conSpec.Image, conSpec.Build, con.Image)
					spec.Jobs[c].Sidecars[s] = conSpec
				}
			}
		}
		for i, img := range imageData.Images {
			if imgSpec, ok := spec.Images[i]; ok {
				imgSpec.Image, imgSpec.Build = assignImage(imgSpec.Image, imgSpec.Build, img.Image)
				spec.Images[i] = imgSpec
			}
		}
		for i, acorn := range imageData.Acorns {
			if acornSpec, ok := spec.Acorns[i]; ok {
				acornSpec.Image, acornSpec.Build = assignAcornImage(acornSpec.Image, acornSpec.Build, acorn.Image)
				spec.Acorns[i] = acornSpec
			}
		}
	}

	return spec, nil
}

func addContainerFiles(fileSet map[string]bool, builds map[string]v1.ContainerImageBuilderSpec, cwd string) {
	for _, build := range builds {
		addContainerFiles(fileSet, build.Sidecars, cwd)
		if build.Build == nil || build.Build.BaseImage != "" {
			continue
		}
		fileSet[filepath.Join(cwd, build.Build.Dockerfile)] = true
	}
}

func addAcorns(fileSet map[string]bool, builds map[string]v1.AcornBuilderSpec, cwd string) {
	for _, build := range builds {
		if build.Build == nil {
			continue
		}
		data, err := cue.ReadCUE(filepath.Join(cwd, build.Build.Acornfile))
		if err != nil {
			return
		}

		appDef, err := NewAppDefinition(data)
		if err != nil {
			return
		}
		files, err := appDef.WatchFiles(filepath.Join(cwd, build.Build.Context))
		if err != nil {
			return
		}
		for _, f := range files {
			fileSet[f] = true
		}
	}
}

func addFiles(fileSet map[string]bool, builds map[string]v1.ImageBuilderSpec, cwd string) {
	for _, build := range builds {
		if build.Build == nil {
			continue
		}
		fileSet[filepath.Join(cwd, build.Build.Dockerfile)] = true
	}
}

func (a *AppDefinition) WatchFiles(cwd string) (result []string, _ error) {
	fileSet := map[string]bool{}
	spec, err := a.BuilderSpec()
	if err != nil {
		return nil, err
	}

	addContainerFiles(fileSet, spec.Containers, cwd)
	addFiles(fileSet, spec.Images, cwd)
	addAcorns(fileSet, spec.Acorns, cwd)

	for k := range fileSet {
		result = append(result, k)
	}
	sort.Strings(result)
	return result, nil
}

func (a *AppDefinition) BuilderSpec() (*v1.BuilderSpec, error) {
	v, err := a.ctx.Transform(BuildTransform)
	if err != nil {
		return nil, err
	}
	spec := &v1.BuilderSpec{}
	return spec, a.ctx.Decode(v, spec)
}

func AppImageFromTar(reader io.Reader) (*v1.AppImage, error) {
	tar := tar.NewReader(reader)
	result := &v1.AppImage{}
	for {
		header, err := tar.Next()
		if err == io.EOF {
			break
		}

		if header.Name == AcornCueFile {
			data, err := ioutil.ReadAll(tar)
			if err != nil {
				return nil, err
			}
			result.Acornfile = string(data)
		} else if header.Name == ImageDataFile {
			err := json.NewDecoder(tar).Decode(&result.ImageData)
			if err != nil {
				return nil, err
			}
		} else if header.Name == BuildDataFile {
			result.BuildParams = map[string]interface{}{}
			err := json.NewDecoder(tar).Decode(&result.BuildParams)
			if err != nil {
				return nil, err
			}
		}
	}

	if result.Acornfile == "" {
		return nil, fmt.Errorf("invalid image, empty acorn.cue")
	}

	return result, nil
}
