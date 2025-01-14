package prowgen

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	cioperatorapi "github.com/openshift/ci-tools/pkg/api"
	"k8s.io/apimachinery/pkg/util/sets"
)

var registryRegex = regexp.MustCompile(`registry\.(|svc\.)ci\.openshift\.org/\S+`)

type orgRepoTag struct {
	Org  string
	Repo string
	Tag  string
}

func (ort orgRepoTag) String() string {
	return ort.Org + "_" + ort.Repo + "_" + ort.Tag
}

func DiscoverImages(r Repository) ReleaseBuildConfigurationOption {
	return func(cfg *cioperatorapi.ReleaseBuildConfiguration) error {
		log.Println(r.RepositoryDirectory(), "Discovering images")
		opts, err := discoverImages(r)
		if err != nil {
			return err
		}

		return applyOptions(cfg, opts...)
	}
}

func discoverImages(r Repository) ([]ReleaseBuildConfigurationOption, error) {
	dockerfiles, err := discoverDockerfiles(r)
	if err != nil {
		return nil, err
	}
	sort.Strings(dockerfiles)

	log.Println(r.RepositoryDirectory(), "Discovered Dockerfiles", dockerfiles)

	options := make([]ReleaseBuildConfigurationOption, 0, len(dockerfiles))

	for _, dockerfile := range dockerfiles {
		requiredBaseImages, inputImages, err := discoverInputImages(dockerfile)
		if err != nil {
			return nil, err
		}

		options = append(options,
			WithBaseImages(requiredBaseImages),
			WithImage(ProjectDirectoryImageBuildStepConfigurationFuncFromImageInput(r, ImageInput{
				Context:        discoverImageContext(dockerfile),
				DockerfilePath: strings.Join(strings.Split(dockerfile, string(os.PathSeparator))[2:], string(os.PathSeparator)),
				Inputs:         inputImages,
			})),
		)
	}

	return options, nil
}

func discoverImageContext(dockerfile string) imageContext {
	context := ProductionContext
	if strings.Contains(dockerfile, "test-images") {
		context = TestContext
	}
	return context
}

func discoverDockerfiles(r Repository) ([]string, error) {
	dir := filepath.Join(r.RepositoryDirectory(), "openshift", "ci-operator")
	dockerfiles, err := filepath.Glob(filepath.Join(dir, "**", "**", "Dockerfile"))
	if err != nil {
		return nil, fmt.Errorf("failed while discovering container images in %s: %w", dir, err)
	}
	return dockerfiles, nil
}

func discoverInputImages(dockerfile string) (map[string]cioperatorapi.ImageStreamTagReference, map[string]cioperatorapi.ImageBuildInputs, error) {
	imagePaths, err := getPullStringsFromDockerfile(dockerfile)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get pull images from dockerfile: %w", err)
	}

	requiredBaseImages := make(map[string]cioperatorapi.ImageStreamTagReference)
	inputImages := make(map[string]cioperatorapi.ImageBuildInputs)

	for _, imagePath := range imagePaths {
		orgRepoTag, err := orgRepoTagFromPullString(imagePath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse string %s as pullspec: %w", imagePath, err)
		}

		requiredBaseImages[orgRepoTag.String()] = cioperatorapi.ImageStreamTagReference{
			Namespace: orgRepoTag.Org,
			Name:      orgRepoTag.Repo,
			Tag:       orgRepoTag.Tag,
		}

		inputs := inputImages[orgRepoTag.String()]
		inputs.As = sets.NewString(inputs.As...).Insert(imagePath).List() //different registries can resolve to the same orgRepoTag
		inputImages[orgRepoTag.String()] = inputs
	}

	return requiredBaseImages, inputImages, nil
}

func getPullStringsFromDockerfile(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open Dockerfile %s: %w", filename, err)
	}
	defer file.Close()

	var images []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "FROM ") {
			continue
		}

		match := registryRegex.FindString(line)
		if match != "" {
			images = append(images, match)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read Dockerfile %s: %w", filename, err)
	}

	return images, nil
}

func orgRepoTagFromPullString(pullString string) (orgRepoTag, error) {
	res := orgRepoTag{Tag: "latest"}
	slashSplit := strings.Split(pullString, "/")
	switch n := len(slashSplit); n {
	case 1:
		res.Org = "_"
		res.Repo = slashSplit[0]
	case 2:
		res.Org = slashSplit[0]
		res.Repo = slashSplit[1]
	case 3:
		res.Org = slashSplit[1]
		res.Repo = slashSplit[2]
	default:
		return res, fmt.Errorf("pull string %q couldn't be parsed, expected to get between one and three elements after slashsplitting, got %d", pullString, n)
	}
	if repoTag := strings.Split(res.Repo, ":"); len(repoTag) == 2 {
		res.Repo = repoTag[0]
		res.Tag = repoTag[1]
	}

	return res, nil
}
