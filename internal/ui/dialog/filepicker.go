package dialog

import (
	"image"
	_ "image/jpeg" // register JPEG format
	_ "image/png"  // register PNG format
	"os"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/home"
	"github.com/stubbedev/harness/internal/ui/common"
	fimage "github.com/stubbedev/harness/internal/ui/image"
	"github.com/stubbedev/harness/internal/ui/keys"
)

// FilePickerID is the identifier for the FilePicker dialog.
const FilePickerID = "filepicker"

// FilePicker is a dialog that allows users to select files or directories.
type FilePicker struct {
	com *common.Common

	imgEnc                      fimage.Encoding
	imgPrevWidth, imgPrevHeight int
	cellSizeW, cellSizeH        int

	fp              filepicker.Model
	previewingImage bool // indicates if an image is being previewed
	isTmux          bool

	km struct {
		Select,
		Down,
		Up,
		Forward,
		Backward,
		Navigate,
		Close key.Binding
	}
}

// CellSize returns the cell size used for image rendering.
func (f *FilePicker) CellSize() fimage.CellSize {
	return fimage.CellSize{
		Width:  f.cellSizeW,
		Height: f.cellSizeH,
	}
}

var _ Dialog = (*FilePicker)(nil)

// NewFilePicker creates a new [FilePicker] dialog.
func NewFilePicker(com *common.Common) (*FilePicker, tea.Cmd) {
	f := new(FilePicker)
	f.com = com

	km := dialogKeys()
	f.km.Select = km.FilePicker.Select
	f.km.Down = km.FilePicker.Down
	f.km.Up = km.FilePicker.Up
	f.km.Forward = km.FilePicker.Forward
	f.km.Backward = km.FilePicker.Backward
	// One help entry stands for all four movement keys, so it follows
	// whatever they are bound to.
	f.km.Navigate = km.FilePicker.Navigate()
	f.km.Close = keys.WithDesc(km.Close, "close/exit")

	fp := filepicker.New()
	fp.AllowedTypes = common.AllowedImageTypes
	fp.ShowPermissions = false
	fp.ShowSize = false
	fp.AutoHeight = false
	fp.Styles = com.Styles.FilePicker
	fp.Cursor = ""
	fp.CurrentDirectory = f.WorkingDir()

	f.fp = fp

	return f, f.fp.Init()
}

// SetImageCapabilities sets the image capabilities for the [FilePicker].
func (f *FilePicker) SetImageCapabilities(caps *common.Capabilities) {
	if caps != nil {
		if caps.SupportsKittyGraphics() {
			f.imgEnc = fimage.EncodingKitty
		}
		f.cellSizeW, f.cellSizeH = caps.CellSize()
		_, f.isTmux = caps.Env.LookupEnv("TMUX")
	}
}

// WorkingDir returns the current working directory of the [FilePicker].
func (f *FilePicker) WorkingDir() string {
	wd := f.com.Workspace.WorkingDir()
	if len(wd) > 0 {
		return wd
	}

	cwd, err := os.Getwd()
	if err != nil {
		return home.Dir()
	}

	return cwd
}

// ShortHelp returns the short help key bindings for the [FilePicker] dialog.
func (f *FilePicker) ShortHelp() []key.Binding {
	return []key.Binding{
		f.km.Navigate,
		f.km.Select,
		f.km.Close,
	}
}

// FullHelp returns the full help key bindings for the [FilePicker] dialog.
func (f *FilePicker) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			f.km.Select,
			f.km.Down,
			f.km.Up,
			f.km.Forward,
		},
		{
			f.km.Backward,
			f.km.Close,
		},
	}
}

// ID returns the identifier of the [FilePicker] dialog.
func (f *FilePicker) ID() string {
	return FilePickerID
}

// HandleMsg updates the [FilePicker] dialog based on the given message.
func (f *FilePicker) HandleMsg(msg tea.Msg) Action {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, f.km.Close):
			return ActionClose{}
		}
	}

	var cmd tea.Cmd
	f.fp, cmd = f.fp.Update(msg)
	if selFile := f.fp.HighlightedPath(); selFile != "" {
		var allowed bool
		for _, allowedExt := range f.fp.AllowedTypes {
			if strings.HasSuffix(strings.ToLower(selFile), allowedExt) {
				allowed = true
				break
			}
		}

		f.previewingImage = allowed
		if allowed && !fimage.HasTransmitted(selFile, f.imgPrevWidth, f.imgPrevHeight) {
			f.previewingImage = false
			img, err := loadImage(selFile)
			if err == nil {
				cmds = append(cmds, tea.Sequence(
					f.imgEnc.Transmit(selFile, img, f.CellSize(), f.imgPrevWidth, f.imgPrevHeight, f.isTmux),
					func() tea.Msg {
						f.previewingImage = true
						return nil
					},
				))
			}
		}
	}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if didSelect, path := f.fp.DidSelectFile(msg); didSelect {
		return ActionFilePickerSelected{Path: path}
	}

	return ActionCmd{tea.Batch(cmds...)}
}

const (
	filePickerMinWidth  = 70
	filePickerMinHeight = 10
)

// Draw renders the [FilePicker] dialog as a string.
func (f *FilePicker) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := f.com.Styles
	width := DialogWidth(t, area)
	height := DialogHeightCeiling(t, area, 10)
	innerWidth := DialogInnerWidth(t, width)

	// Scale down image preview on small screens. Hide it entirely
	// when the dialog is too short to show both preview and files.
	imgPrevHeight := 0
	if height > 5 {
		imgPrevHeight = filePickerMinHeight*2 - t.Dialog.ImagePreview.GetVerticalFrameSize()
	}
	imgPrevWidth := innerWidth - t.Dialog.ImagePreview.GetHorizontalFrameSize()
	f.imgPrevWidth = imgPrevWidth
	f.imgPrevHeight = imgPrevHeight
	f.fp.SetHeight(height)

	styles := t.FilePicker
	styles.File = styles.File.Width(innerWidth)
	styles.Directory = styles.Directory.Width(innerWidth)
	styles.Selected = styles.Selected.Width(innerWidth)
	styles.DisabledSelected = styles.DisabledSelected.Width(innerWidth)
	f.fp.Styles = styles

	rc := NewRenderContext(t, width)
	rc.Gap = 1
	rc.Title = "Add Image"

	// The preview renders only when an image is actually on screen; a
	// placeholder block would read as a stray background panel.
	if imgPrevHeight > 0 && f.previewingImage {
		path := f.fp.HighlightedPath()
		imgPreview := t.Dialog.ImagePreview.Align(lipgloss.Center).Width(innerWidth).Render(f.imgEnc.Render(path, imgPrevWidth, imgPrevHeight))
		rc.AddPart(imgPreview)
	}

	files := strings.TrimSpace(f.fp.View())
	rc.AddPart(files)

	view := rc.Render()

	DrawCenter(scr, area, view)
	return nil
}

func loadImage(path string) (img image.Image, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	img, _, err = image.Decode(file)
	if err != nil {
		return nil, err
	}

	return img, nil
}
