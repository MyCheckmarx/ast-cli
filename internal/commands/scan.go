package commands

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/errors"

	commonParams "github.com/checkmarxDev/ast-cli/internal/params"
	"github.com/checkmarxDev/ast-cli/internal/wrappers"
	scansRESTApi "github.com/checkmarxDev/scans/pkg/api/scans/rest/v1"

	"github.com/mssola/user_agent"
	"github.com/spf13/cobra"
)

const (
	failedCreating    = "Failed creating a scan"
	failedGetting     = "Failed showing a scan"
	failedGettingTags = "Failed getting tags"
	failedDeleting    = "Failed deleting a scan"
	failedCanceling   = "Failed canceling a scan"
	failedGettingAll  = "Failed listing"
)

var (
	filterScanListFlagUsage = fmt.Sprintf("Filter the list of scans. Use ';' as the delimeter for arrays. Available filters are: %s",
		strings.Join([]string{
			commonParams.LimitQueryParam,
			commonParams.OffsetQueryParam,
			commonParams.ScanIDsQueryParam,
			commonParams.TagsKeyQueryParam,
			commonParams.TagsValueQueryParam,
			commonParams.StatusesQueryParam,
			commonParams.ProjectIDQueryParam,
			commonParams.FromDateQueryParam,
			commonParams.ToDateQueryParam}, ","))
)

func NewScanCommand(scansWrapper wrappers.ScansWrapper, uploadsWrapper wrappers.UploadsWrapper) *cobra.Command {
	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Manage scans",
	}

	createScanCmd := &cobra.Command{
		Use:   "create",
		Short: "Create and run a new scan",
		RunE:  runCreateScanCommand(scansWrapper, uploadsWrapper),
	}
	createScanCmd.PersistentFlags().StringP(sourcesFlag, sourcesFlagSh, "",
		"A path to the sources file to scan")
	createScanCmd.PersistentFlags().StringP(sourceDirFlag, sourceDirFlagSh, "",
		"A path to directory with sources to scan")
	createScanCmd.PersistentFlags().StringP(sourceDirFilterFlag, sourceDirFilterFlagSh, "",
		"Source file GLOB filtering pattern")
	createScanCmd.PersistentFlags().StringP(sourceExclusionFilterFlag, sourceExclusionFilterFlagSh, "",
		"Source file exclusion pattern")
	createScanCmd.PersistentFlags().StringP(inputFileFlag, inputFileFlagSh, "",
		"A file holding the requested scan object in JSON format. Takes precedence over --input")
	createScanCmd.PersistentFlags().String(projectName, "", "Name of the project")
	createScanCmd.PersistentFlags().String(incremental, "", "Indicates if incremental scan should be performed, defaults to false.")
	createScanCmd.PersistentFlags().String(presetName, "", "The name of the Checkmarx preset to use.")
	createScanCmd.PersistentFlags().String(projectSourceType, "", "Type of project source: upload")
	createScanCmd.PersistentFlags().String(projectType, "", "Type of project: sast")
	listScansCmd := &cobra.Command{
		Use:   "list",
		Short: "List all scans in the system",
		RunE:  runListScansCommand(scansWrapper),
	}
	listScansCmd.PersistentFlags().StringSlice(filterFlag, []string{}, filterScanListFlagUsage)

	showScanCmd := &cobra.Command{
		Use:   "show <scan id>",
		Short: "Show information about a scan",
		RunE:  runGetScanByIDCommand(scansWrapper),
	}

	workflowScanCmd := &cobra.Command{
		Use:   "workflow <scan id>",
		Short: "Show information about a scan workflow",
		RunE:  runScanWorkflowByIDCommand(scansWrapper),
	}

	deleteScanCmd := &cobra.Command{
		Use:   "delete [scan id...]",
		Short: "Deletes one or more scans",
		RunE:  runDeleteScanCommand(scansWrapper),
	}

	cacnelScanCmd := &cobra.Command{
		Use:   "cancel [scan id...]",
		Short: "Cancel one or more scans from running",
		RunE:  runCancelScanCommand(scansWrapper),
	}

	tagsCmd := &cobra.Command{
		Use:   "tags",
		Short: "Get a list of all available tags to filter by",
		RunE:  runGetTagsCommand(scansWrapper),
	}

	addFormatFlagToMultipleCommands([]*cobra.Command{createScanCmd, listScansCmd, showScanCmd, workflowScanCmd},
		formatTable, formatList, formatJSON)
	scanCmd.AddCommand(createScanCmd, showScanCmd, workflowScanCmd, listScansCmd, deleteScanCmd, cacnelScanCmd, tagsCmd)
	return scanCmd
}

func updateScanRequestValues(input *[]byte, cmd *cobra.Command) {
	var info map[string]interface{}
	newProjectName, _ := cmd.Flags().GetString(projectName)
	newProjectSourcType, _ := cmd.Flags().GetString(projectSourceType)
	newProjectType, _ := cmd.Flags().GetString(projectType)
	newIncremental, _ := cmd.Flags().GetString(incremental)
	newPresetName, _ := cmd.Flags().GetString(presetName)
	_ = json.Unmarshal(*input, &info)
	// Handle the project settings
	if info["project"] == nil {
		var projectMap map[string]interface{}
		_ = json.Unmarshal([]byte("{}"), &projectMap)
		info["project"] = projectMap
	}
	if newProjectName != "" {
		info["project"].(map[string]interface{})["id"] = newProjectName
	}
	if newProjectSourcType != "" {
		info["project"].(map[string]interface{})["type"] = newProjectSourcType
	}
	// Handle the scan configuration
	if info["config"] == nil {
		var configArr []interface{}
		_ = json.Unmarshal([]byte("[{}]"), &configArr)
		info["config"] = configArr
	}
	if newProjectType != "" {
		info["config"].([]interface{})[0].(map[string]interface{})["type"] = newProjectType
	}
	if info["config"].([]interface{})[0].(map[string]interface{})["value"] == nil {
		var valueMap map[string]interface{}
		_ = json.Unmarshal([]byte("{}"), &valueMap)
		info["config"].([]interface{})[0].(map[string]interface{})["value"] = valueMap
	}
	if newIncremental != "" {
		info["config"].([]interface{})[0].(map[string]interface{})["value"].(map[string]interface{})["incremental"] = newIncremental
	}
	if newPresetName != "" {
		info["config"].([]interface{})[0].(map[string]interface{})["value"].(map[string]interface{})["presetName"] = newPresetName
	}
	*input, _ = json.Marshal(info)
}

func compressFolder(sourceDir string, filter string, sourceExclusionFilter string) (string, error) {
	var err error
	var filters []string = nil
	var exclusions []string = nil
	if len(filter) > 0 {
		filters = strings.Split(filter, ",")
	}
	if len(sourceExclusionFilter) > 0 {
		exclusions = strings.Split(sourceExclusionFilter, ",")
	}
	outputFile, err := ioutil.TempFile(os.TempDir(), "cx-*.zip")
	if err != nil {
		log.Fatal("Cannot source code temp file.", err)
	}
	//defer os.Remove(outputFile.Name())
	zip := zip.NewWriter(outputFile)
	sourceDir += "/"
	addDirFiles(zip, "/", sourceDir, filters, exclusions)
	fmt.Println("Zipped File:", outputFile.Name())
	fmt.Println("source DIR: ", sourceDir)
	fmt.Println("GLOB pattr", filter)
	// Close the file
	if err := zip.Close(); err != nil {
		log.Fatal(err)
	}
	return outputFile.Name(), nil
}

func filterMatched(filters []string, fileName string) (bool, bool) {
	var matched = true
	var firstMatch = true
	var excluded = false
	for _, filter := range filters {
		if filter[0] == '!' {
			// it just needs to match one exclusion to be excluded.
			if !excluded {
				excluded, _ = path.Match(filter[1:], fileName)
			}
		} else {
			// 1. If there are no inclusions everythng is considered included
			// 2. If there is at least one included and nothing matches the its
			// not included
			// it just needs to match one thing to be matched
			if firstMatch {
				// Need to reset the match flag because now we need to find one.
				matched = false
				firstMatch = false
			}
			if !matched {
				matched, _ = path.Match(filter, fileName)
			}
		}
	}
	return matched, excluded
}

func addDirFiles(zip *zip.Writer, baseDir string, parentDir string, filters []string, exclusions []string) {
	files, err := ioutil.ReadDir(parentDir)
	if err != nil {
		fmt.Println(err)
	}
	for _, file := range files {
		fileName := parentDir + file.Name()
		if !file.IsDir() {
			var matched = true
			var excluded = false
			if filters != nil {
				matched, excluded = filterMatched(filters, file.Name())
			}
			// Exclusions have been moved to filters list
			//if exclusions != nil {
			//	excluded = filterMatched(exclusions, file.Name())
			//}
			if matched && !excluded {
				fmt.Println("Included: ", fileName)
				dat, err := ioutil.ReadFile(parentDir + file.Name())
				if err != nil {
					fmt.Println(err)
				}
				f, err := zip.Create(baseDir + file.Name())
				if err != nil {
					fmt.Println(err)
				}
				_, err = f.Write(dat)
				if err != nil {
					fmt.Println(err)
				}
			} else {
				fmt.Println("Excluded: ", fileName)
			}
		} else if file.IsDir() {
			fmt.Println("Directory: ", fileName)
			newParent := parentDir + file.Name() + "/"
			newBase := baseDir + file.Name() + "/"
			addDirFiles(zip, newBase, newParent, filters, exclusions)
		}
	}
}

func determineSourceType(
	uploadsWrapper wrappers.UploadsWrapper,
	sourcesFile string,
	sourceDir string,
	sourceDirFilter string,
	sourceExclusionFilter string) (string, error) {
	var err error
	var preSignedURL string
	var error error = nil
	if sourceDir != "" {
		sourcesFile, _ = compressFolder(sourceDir, sourceDirFilter, sourceExclusionFilter)
	}
	fmt.Println("Moving forward with this source file: ", sourcesFile)
	if sourcesFile != "" {
		// Send a request to uploads service
		var preSignedURL *string
		preSignedURL, err = uploadsWrapper.UploadFile(sourcesFile)
		if err != nil {
			return "", errors.Wrapf(err, "%s: Failed to upload sources file\n", failedCreating)
		}
		PrintIfVerbose(fmt.Sprintf("Uploading file to %s\n", *preSignedURL))
		return *preSignedURL, error
	}
	return preSignedURL, error
}

func runCreateScanCommand(scansWrapper wrappers.ScansWrapper,
	uploadsWrapper wrappers.UploadsWrapper) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		var input []byte
		var err error
		scanInputFile, _ := cmd.Flags().GetString(inputFileFlag)
		sourcesFile, _ := cmd.Flags().GetString(sourcesFlag)
		sourceDir, _ := cmd.Flags().GetString(sourceDirFlag)
		sourceDirFilter, _ := cmd.Flags().GetString(sourceDirFilterFlag)
		sourceExclusionFilter, _ := cmd.Flags().GetString(sourceExclusionFilterFlag)
		if scanInputFile != "" {
			// Reading from input file
			input, err = ioutil.ReadFile(scanInputFile)
			if err != nil {
				return errors.Wrapf(err, "%s: Failed to open input file", failedCreating)
			}
		} else {
			input = []byte("{}")
		}

		updateScanRequestValues(&input, cmd)
		fmt.Println(string(input))
		var scanModel = scansRESTApi.Scan{}
		var scanResponseModel *scansRESTApi.ScanResponseModel
		var errorModel *scansRESTApi.ErrorModel
		// Try to parse to a scan model in order to manipulate the request payload
		err = json.Unmarshal(input, &scanModel)
		if err != nil {
			return errors.Wrapf(err, "%s: Input in bad format", failedCreating)
		}
		scanModel.UploadURL, err = determineSourceType(uploadsWrapper, sourcesFile, sourceDir, sourceDirFilter, sourceExclusionFilter)
		if err != nil {
			return err
		}
		var payload []byte
		payload, _ = json.Marshal(scanModel)
		PrintIfVerbose(fmt.Sprintf("Payload to scans service: %s\n", string(payload)))

		scanResponseModel, errorModel, err = scansWrapper.Create(&scanModel)
		if err != nil {
			return errors.Wrapf(err, "%s", failedCreating)
		}

		// Checking the response
		if errorModel != nil {
			return errors.Errorf("%s: CODE: %d, %s\n", failedCreating, errorModel.Code, errorModel.Message)
		} else if scanResponseModel != nil {
			err = printByFormat(cmd, toScanView(scanResponseModel))
			if err != nil {
				return errors.Wrapf(err, "%s\n", failedCreating)
			}
		}
		return nil
	}
}

func runListScansCommand(scansWrapper wrappers.ScansWrapper) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		var allScansModel *scansRESTApi.ScansCollectionResponseModel
		var errorModel *scansRESTApi.ErrorModel
		params, err := getFilters(cmd)
		if err != nil {
			return errors.Wrapf(err, "%s", failedGettingAll)
		}

		allScansModel, errorModel, err = scansWrapper.Get(params)
		if err != nil {
			return errors.Wrapf(err, "%s\n", failedGettingAll)
		}
		// Checking the response
		if errorModel != nil {
			return errors.Errorf("%s: CODE: %d, %s\n", failedGettingAll, errorModel.Code, errorModel.Message)
		} else if allScansModel != nil && allScansModel.Scans != nil {
			err = printByFormat(cmd, toScanViews(allScansModel.Scans))
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func runGetScanByIDCommand(scansWrapper wrappers.ScansWrapper) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		var scanResponseModel *scansRESTApi.ScanResponseModel
		var errorModel *scansRESTApi.ErrorModel
		var err error
		if len(args) == 0 {
			return errors.Errorf("%s: Please provide a scan ID", failedGetting)
		}
		scanID := args[0]
		scanResponseModel, errorModel, err = scansWrapper.GetByID(scanID)
		if err != nil {
			return errors.Wrapf(err, "%s", failedGetting)
		}
		// Checking the response
		if errorModel != nil {
			return errors.Errorf("%s: CODE: %d, %s", failedGetting, errorModel.Code, errorModel.Message)
		} else if scanResponseModel != nil {
			err = printByFormat(cmd, toScanView(scanResponseModel))
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func runScanWorkflowByIDCommand(scansWrapper wrappers.ScansWrapper) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		var taskResponseModel []*wrappers.ScanTaskResponseModel
		var errorModel *scansRESTApi.ErrorModel
		var err error
		if len(args) == 0 {
			return errors.Errorf("%s: Please provide a scan ID", failedGetting)
		}
		scanID := args[0]
		taskResponseModel, errorModel, err = scansWrapper.GetWorkflowByID(scanID)
		if err != nil {
			return errors.Wrapf(err, "%s", failedGetting)
		}
		// Checking the response
		if errorModel != nil {
			return errors.Errorf("%s: CODE: %d, %s", failedGetting, errorModel.Code, errorModel.Message)
		} else if taskResponseModel != nil {
			err = printByFormat(cmd, taskResponseModel)
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func runDeleteScanCommand(scansWrapper wrappers.ScansWrapper) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.Errorf("%s: Please provide at least one scan ID", failedDeleting)
		}

		for _, scanID := range args {
			errorModel, err := scansWrapper.Delete(scanID)
			if err != nil {
				return errors.Wrapf(err, "%s\n", failedDeleting)
			}

			// Checking the response
			if errorModel != nil {
				return errors.Errorf("%s: CODE: %d, %s\n", failedDeleting, errorModel.Code, errorModel.Message)
			}
		}

		return nil
	}
}

func runCancelScanCommand(scansWrapper wrappers.ScansWrapper) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.Errorf("%s: Please provide at least one scan ID", failedCanceling)
		}

		for _, scanID := range args {
			errorModel, err := scansWrapper.Cancel(scanID)
			if err != nil {
				return errors.Wrapf(err, "%s\n", failedCanceling)
			}

			// Checking the response
			if errorModel != nil {
				return errors.Errorf("%s: CODE: %d, %s\n", failedCanceling, errorModel.Code, errorModel.Message)
			}
		}

		return nil
	}
}

func runGetTagsCommand(scansWrapper wrappers.ScansWrapper) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		var tags map[string][]string
		var errorModel *scansRESTApi.ErrorModel
		var err error
		tags, errorModel, err = scansWrapper.Tags()
		if err != nil {
			return errors.Wrapf(err, "%s", failedGettingTags)
		}
		// Checking the response
		if errorModel != nil {
			return errors.Errorf("%s: CODE: %d, %s", failedGettingTags, errorModel.Code, errorModel.Message)
		} else if tags != nil {
			var tagsJSON []byte
			tagsJSON, err = json.Marshal(tags)
			if err != nil {
				return errors.Wrapf(err, "%s: failed to serialize scan tags response ", failedGettingTags)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(tagsJSON))
		}
		return nil
	}
}

type scanView struct {
	ID        string `format:"name:Scan ID"`
	ProjectID string `format:"name:Project ID"`
	Status    string
	CreatedAt time.Time `format:"name:Created at;time:01-02-06 15:04:05"`
	UpdatedAt time.Time `format:"name:Updated at;time:01-02-06 15:04:05"`
	Tags      map[string]string
	Initiator string
	Origin    string
}

func toScanViews(scans []scansRESTApi.ScanResponseModel) []*scanView {
	views := make([]*scanView, len(scans))
	for i := 0; i < len(scans); i++ {
		views[i] = toScanView(&scans[i])
	}
	return views
}

func toScanView(scan *scansRESTApi.ScanResponseModel) *scanView {
	var origin string
	if scan.UserAgent != "" {
		ua := user_agent.New(scan.UserAgent)
		name, version := ua.Browser()
		origin = name + " " + version[:strings.Index(version, ".")] // Takes the major
	}

	return &scanView{
		ID:        scan.ID,
		Status:    string(scan.Status),
		CreatedAt: scan.CreatedAt,
		UpdatedAt: scan.UpdatedAt,
		ProjectID: scan.ProjectID,
		Tags:      scan.Tags,
		Initiator: scan.Initiator,
		Origin:    origin,
	}
}
