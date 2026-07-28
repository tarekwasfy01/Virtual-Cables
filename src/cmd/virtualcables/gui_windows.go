//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	procRegisterClassEx      = user32.NewProc("RegisterClassExW")
	procCreateWindowEx       = user32.NewProc("CreateWindowExW")
	procDefWindowProc        = user32.NewProc("DefWindowProcW")
	procShowWindow           = user32.NewProc("ShowWindow")
	procUpdateWindow         = user32.NewProc("UpdateWindow")
	procGetMessage           = user32.NewProc("GetMessageW")
	procTranslateMessage     = user32.NewProc("TranslateMessage")
	procDispatchMessage      = user32.NewProc("DispatchMessageW")
	procPostQuitMessage      = user32.NewProc("PostQuitMessage")
	procSendMessage          = user32.NewProc("SendMessageW")
	procMoveWindow           = user32.NewProc("MoveWindow")
	procMessageBox           = user32.NewProc("MessageBoxW")
	procLoadIcon             = user32.NewProc("LoadIconW")
	procLoadImage            = user32.NewProc("LoadImageW")
	procEnableWindow         = user32.NewProc("EnableWindow")
	procSetClassLongPtr      = user32.NewProc("SetClassLongPtrW")
	procGetModuleHandle      = kernel32.NewProc("GetModuleHandleW")
	procShellExecute         = shell32.NewProc("ShellExecuteW")
	procSHGetKnownFolderPath = shell32.NewProc("SHGetKnownFolderPath")
	procSetAppUserModelID    = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procExtractIconEx        = shell32.NewProc("ExtractIconExW")
	procGetStockObject       = gdi32.NewProc("GetStockObject")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
)

type windowsGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var folderIDDownloads = windowsGUID{
	Data1: 0x374DE290,
	Data2: 0x123F,
	Data3: 0x4565,
	Data4: [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B},
}

type point struct{ X, Y int32 }
type msg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             point
	Private        uint32
}
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   uintptr
	ClassName  uintptr
	IconSm     uintptr
}

const (
	wmCreate  = 0x0001
	wmDestroy = 0x0002
	wmSize    = 0x0005
	wmCommand = 0x0111
	wmSetIcon = 0x0080
	wmSetText = 0x000C
	wmSetFont = 0x0030

	iconSmall      = 0
	iconBig        = 1
	imageIcon      = 1
	lrLoadFromFile = 0x0010
	gclpHIcon      = -14
	gclpHIconSm    = -34

	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsTabStop          = 0x00010000
	wsVScroll          = 0x00200000
	wsBorder           = 0x00800000

	bsPushButton = 0
	lbsNotify    = 1
	lbnSelChange = 1

	swShow       = 5
	swShowNormal = 1
	cwUseDefault = 0x80000000

	idAdd       = 1001
	idRemove    = 1002
	idList      = 1003
	idStatus    = 1004
	idDriver    = 1005
	idDownloads = 1006

	lbAddString    = 0x0180
	lbResetContent = 0x0184
	lbGetCurSel    = 0x0188
	lbSetCurSel    = 0x0186

	colorWindow    = 5
	idiApplication = 32512
	defaultGUIFont = 17
)

var guiManager *appServer
var mainWindow, addButton, removeButton, driverButton, downloadsButton, listBox, statusText uintptr
var cableCount int
var appIconBig, appIconSmall uintptr

func utf16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func loword(v uintptr) uint16           { return uint16(v & 0xffff) }
func hiword(v uintptr) uint16           { return uint16((v >> 16) & 0xffff) }
func makeIntResource(id uint16) uintptr { return uintptr(id) }

func runGUI(manager *appServer) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	guiManager = manager
	cableCount = manager.count

	// Give the taskbar a stable application identity even when launched from an MSIX.
	appID := utf16("TarekWasfy.VirtualCables")
	procSetAppUserModelID.Call(uintptr(unsafe.Pointer(appID)))

	instance, _, _ := procGetModuleHandle.Call(0)
	appIconBig, appIconSmall = loadApplicationIcons(instance)
	if appIconBig == 0 {
		appIconBig, _, _ = procLoadIcon.Call(0, idiApplication)
	}
	if appIconSmall == 0 {
		appIconSmall = appIconBig
	}

	className := utf16("VirtualCablesNativeWindow")
	wc := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:    syscall.NewCallback(windowProc),
		Instance:   instance,
		Icon:       appIconBig,
		IconSm:     appIconSmall,
		Background: colorWindow + 1,
		ClassName:  uintptr(unsafe.Pointer(className)),
	}
	if r, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		showError("Could not register the native window class.")
		return
	}

	title := utf16("Virtual Cables")
	mainWindow, _, _ = procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsVisible,
		cwUseDefault, cwUseDefault, 820, 540,
		0, 0, instance, 0,
	)
	if mainWindow == 0 {
		showError("Could not create the Virtual Cables window.")
		return
	}

	// Explicitly set both the window-class icons and the per-window icons.
	// Windows can otherwise keep showing the generic Go executable icon in the
	// title bar or taskbar even though the PE resource contains our ICO.
	setWindowAndClassIcons(mainWindow, appIconBig, appIconSmall)

	procShowWindow.Call(mainWindow, swShow)
	procUpdateWindow.Call(mainWindow)
	setWindowAndClassIcons(mainWindow, appIconBig, appIconSmall)
	startCableBroker()

	if startError := manager.LastError(); startError != "" {
		showError("The Virtual Cables window is now staying open, but the USB/IP server could not start:\n\n" + startError + "\n\nDiagnostic log:\n" + manager.LogPath())
	}

	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func loadApplicationIcons(instance uintptr) (uintptr, uintptr) {
	// First use the deterministic resource ID generated by rsrc.
	large := loadEmbeddedIcon(instance, 32, 32)
	small := loadEmbeddedIcon(instance, 16, 16)
	if large != 0 || small != 0 {
		if large == 0 {
			large = small
		}
		if small == 0 {
			small = large
		}
		return large, small
	}

	// Also support the copied ICO as a fallback. This makes the GUI icon
	// reliable even if a resource compiler was skipped by an older build.
	if exe, err := os.Executable(); err == nil {
		icoPath := filepath.Join(filepath.Dir(exe), "ICON", "Virtual Cables.ico")
		if _, err := os.Stat(icoPath); err == nil {
			large, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(utf16(icoPath))), imageIcon, 32, 32, lrLoadFromFile)
			small, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(utf16(icoPath))), imageIcon, 16, 16, lrLoadFromFile)
			if large != 0 || small != 0 {
				if large == 0 {
					large = small
				}
				if small == 0 {
					small = large
				}
				return large, small
			}
		}
	}

	// Finally extract the first icon from the running EXE.
	if exe, err := os.Executable(); err == nil {
		var large, small uintptr
		path := utf16(exe)
		count, _, _ := procExtractIconEx.Call(
			uintptr(unsafe.Pointer(path)),
			0,
			uintptr(unsafe.Pointer(&large)),
			uintptr(unsafe.Pointer(&small)),
			1,
		)
		if count > 0 && (large != 0 || small != 0) {
			if large == 0 {
				large = small
			}
			if small == 0 {
				small = large
			}
			return large, small
		}
	}

	return 0, 0
}

func signedIndex(v int32) uintptr { return uintptr(int64(v)) }

func setWindowAndClassIcons(hwnd, large, small uintptr) {
	if hwnd == 0 {
		return
	}
	if large != 0 {
		procSetClassLongPtr.Call(hwnd, signedIndex(gclpHIcon), large)
		procSendMessage.Call(hwnd, wmSetIcon, iconBig, large)
	}
	if small != 0 {
		procSetClassLongPtr.Call(hwnd, signedIndex(gclpHIconSm), small)
		procSendMessage.Call(hwnd, wmSetIcon, iconSmall, small)
	}
}

func loadEmbeddedIcon(instance uintptr, width, height int32) uintptr {
	icon, _, _ := procLoadImage.Call(
		instance,
		makeIntResource(1),
		imageIcon,
		uintptr(width),
		uintptr(height),
		0,
	)
	if icon != 0 {
		return icon
	}
	icon, _, _ = procLoadIcon.Call(instance, makeIntResource(1))
	return icon
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCreate:
		addButton = createControl("BUTTON", "Add Cable", wsChild|wsVisible|wsTabStop|bsPushButton, 12, 12, 112, 34, hwnd, idAdd)
		removeButton = createControl("BUTTON", "Remove Cable", wsChild|wsVisible|wsTabStop|bsPushButton, 134, 12, 122, 34, hwnd, idRemove)
		downloadsButton = createControl("BUTTON", "Open Downloads", wsChild|wsVisible|wsTabStop|bsPushButton, 266, 8, 200, 42, hwnd, idDownloads)
		driverButton = createControl("BUTTON", "Download and Install Driver", wsChild|wsVisible|wsTabStop|bsPushButton, 532, 8, 260, 42, hwnd, idDriver)
		listBox = createControl("LISTBOX", "", wsChild|wsVisible|wsVScroll|wsBorder|lbsNotify, 12, 60, 780, 402, hwnd, idList)
		statusText = createControl("STATIC", "", wsChild|wsVisible, 12, 474, 780, 24, hwnd, idStatus)
		refreshList()
		return 0

	case wmSize:
		w := int32(loword(lParam))
		h := int32(hiword(lParam))
		driverX := max32(476, w-272)
		downloadsX := max32(266, driverX-210)
		procMoveWindow.Call(downloadsButton, uintptr(downloadsX), 8, 200, 42, 1)
		procMoveWindow.Call(driverButton, uintptr(driverX), 8, 260, 42, 1)
		procMoveWindow.Call(listBox, 12, 60, uintptr(max32(100, w-24)), uintptr(max32(100, h-126)), 1)
		procMoveWindow.Call(statusText, 12, uintptr(max32(80, h-48)), uintptr(max32(100, w-24)), 24, 1)
		return 0

	case wmCommand:
		id := int(loword(wParam))
		code := int(hiword(wParam))
		if id == idAdd {
			addCable()
			return 0
		}
		if id == idRemove {
			removeCable()
			return 0
		}
		if id == idDriver {
			downloadAndInstallDriver()
			return 0
		}
		if id == idDownloads {
			openDriverDownloadsFromGUI()
			return 0
		}
		if id == idList && code == lbnSelChange {
			return 0
		}

	case wmDestroy:
		stopCableBroker()
		if guiManager != nil {
			guiManager.Stop()
		}
		procPostQuitMessage.Call(0)
		return 0
	}

	r, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func createControl(class, text string, style uintptr, x, y, w, h int32, parent uintptr, id int) uintptr {
	r, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(utf16(class))),
		uintptr(unsafe.Pointer(utf16(text))),
		style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, uintptr(id), 0, 0,
	)
	if r != 0 {
		font, _, _ := procGetStockObject.Call(defaultGUIFont)
		if font != 0 {
			procSendMessage.Call(r, wmSetFont, font, 1)
		}
	}
	return r
}

func refreshList() {
	procSendMessage.Call(listBox, lbResetContent, 0, 0)
	serverStatus := "USB/IP server ready"
	if guiManager != nil && guiManager.LastError() != "" {
		serverStatus = "USB/IP server error - open log"
	}
	for i := 1; i <= cableCount; i++ {
		line := fmt.Sprintf("Virtual Cable %02d        Bus ID 1-%d        %s", i, i, serverStatus)
		procSendMessage.Call(listBox, lbAddString, 0, uintptr(unsafe.Pointer(utf16(line))))
	}
	if cableCount > 0 {
		procSendMessage.Call(listBox, lbSetCurSel, uintptr(cableCount-1), 0)
	}
	if guiManager != nil && guiManager.LastError() != "" {
		setStatus("The window remains open. USB/IP server error: " + guiManager.LastError())
		return
	}
	setStatus(fmt.Sprintf("%d cable(s) configured. USB/IP server is running.", cableCount))
}

func addCable() {
	if cableCount >= 32 {
		showError("A maximum of 32 virtual cables is supported.")
		return
	}
	applyCount(cableCount + 1)
}

func removeCable() {
	if cableCount <= 1 {
		showError("At least one virtual cable must remain.")
		return
	}
	applyCount(cableCount - 1)
}

func applyCount(n int) {
	if err := guiManager.Restart(n); err != nil {
		showError("Could not restart the USB/IP server:\n" + err.Error())
		return
	}
	cableCount = n
	if err := saveCableCount(n); err != nil {
		showError("The cable was changed, but CONFIG.ini could not be saved:\n" + err.Error())
	}
	refreshList()
	requestCableSync()
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

type releaseAsset struct {
	Name string
	URL  string
	Size int64
}

func downloadAndInstallDriver() {
	procEnableWindow.Call(driverButton, 0)
	setStatus("Checking the official usbip-win2 release...")

	go func() {
		if guiManager != nil && guiManager.logger != nil {
			guiManager.logger.Printf("Driver installation requested from the GUI")
		}
		assetPath, assetName, err := downloadLatestUSBIPInstaller()
		if err != nil {
			procEnableWindow.Call(driverButton, 1)
			openURL(usbipReleasePage)
			setStatus("Automatic download failed. The official release page was opened.")
			message := "The automatic usbip-win2 download could not reach GitHub.\n\n" +
				"The official release page has been opened in your browser. Download and run the Windows x64 installer there."
			if isNameResolutionError(err) {
				message += "\n\nWindows reported a DNS name-resolution error. Check the Internet connection, DNS settings, firewall, VPN, or proxy on this PC."
			}
			message += "\n\nTechnical details:\n" + err.Error()
			showError(message)
			return
		}

		setStatus("Download complete. Verifying the installer signature...")
		if signatureErr := verifyAuthenticodeSignature(assetPath); signatureErr != nil {
			if guiManager != nil && guiManager.logger != nil {
				guiManager.logger.Printf("Installer Authenticode warning; continuing with the official GitHub asset: %v", signatureErr)
			}
			if folderErr := revealDownloadedFile(assetPath); folderErr != nil && guiManager != nil && guiManager.logger != nil {
				guiManager.logger.Printf("Could not reveal downloaded installer before launch: %v", folderErr)
			}
			setStatus("The publisher signature could not be verified. The download folder was opened; asking Windows to start the installer...")
		} else {
			setStatus("Signature valid. Starting the driver installer...")
		}

		if err := startInstallerElevated(assetPath); err != nil {
			procEnableWindow.Call(driverButton, 1)
			folderErr := revealDownloadedFile(assetPath)
			setStatus("The installer could not be started. Its download folder was opened.")
			message := "The installer was downloaded but Windows could not start it. The download folder has been opened and the installer selected.\n\nDownloaded file:\n" + assetPath + "\n\nStart error:\n" + err.Error()
			if folderErr != nil {
				message += "\n\nThe folder could not be opened automatically:\n" + folderErr.Error()
			}
			showError(message)
			return
		}

		procEnableWindow.Call(driverButton, 1)
		setStatus("usbip-win2 setup started: " + assetName + ". Accept the administrator prompt and reboot if requested.")
	}()
}

const usbipReleasePage = "https://github.com/vadimgrn/usbip-win2/releases/latest"

func isNameResolutionError(err error) bool {
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return dnsError.IsNotFound || dnsError.IsTemporary || dnsError.IsTimeout
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no such host") ||
		strings.Contains(text, "name resolution") ||
		strings.Contains(text, "servername oder serveradresse") ||
		strings.Contains(text, "host is unknown")
}

func downloadLatestUSBIPInstaller() (string, string, error) {
	const apiURL = "https://api.github.com/repos/vadimgrn/usbip-win2/releases/latest"

	client := &http.Client{
		Timeout: 4 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return errors.New("too many HTTP redirects")
			}
			return validateGitHubDownloadURL(req.URL.String())
		},
	}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Virtual-Cables/0.5.13")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GitHub release query failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub release query returned HTTP %s", resp.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&release); err != nil {
		return "", "", fmt.Errorf("invalid GitHub release response: %w", err)
	}

	asset, err := chooseWindowsX64Installer(release)
	if err != nil {
		if release.HTMLURL != "" {
			openURL(release.HTMLURL)
		}
		return "", "", err
	}

	downloadDir := driverDownloadsDir()
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create download folder: %w", err)
	}

	name := filepath.Base(asset.Name)
	if name == "." || name == "" {
		return "", "", errors.New("the release asset has no valid filename")
	}
	finalPath := filepath.Join(downloadDir, name)
	tempPath := finalPath + ".part"
	_ = os.Remove(tempPath)

	setStatus("Downloading " + name + "...")
	if err := validateGitHubDownloadURL(asset.URL); err != nil {
		return "", "", err
	}
	if guiManager != nil && guiManager.logger != nil {
		guiManager.logger.Printf("Downloading official GitHub release asset: %s", asset.URL)
	}
	downloadReq, err := http.NewRequest(http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", "", err
	}
	downloadReq.Header.Set("User-Agent", "Virtual-Cables/0.5.13")
	downloadResp, err := client.Do(downloadReq)
	if err != nil {
		return "", "", fmt.Errorf("installer download failed: %w", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("installer download returned HTTP %s", downloadResp.Status)
	}

	out, err := os.Create(tempPath)
	if err != nil {
		return "", "", fmt.Errorf("create temporary installer: %w", err)
	}
	written, copyErr := io.Copy(out, downloadResp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return "", "", fmt.Errorf("write installer: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return "", "", fmt.Errorf("close installer: %w", closeErr)
	}
	if asset.Size > 0 && written != asset.Size {
		_ = os.Remove(tempPath)
		return "", "", fmt.Errorf("download size mismatch: expected %d bytes, received %d", asset.Size, written)
	}
	if written < 64*1024 {
		_ = os.Remove(tempPath)
		return "", "", fmt.Errorf("downloaded installer is unexpectedly small: %d bytes", written)
	}

	_ = os.Remove(finalPath)
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return "", "", fmt.Errorf("finish installer download: %w", err)
	}
	return finalPath, name, nil
}

func chooseWindowsX64Installer(release githubRelease) (releaseAsset, error) {
	bestScore := -1
	var best releaseAsset
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if asset.BrowserDownloadURL == "" {
			continue
		}
		if strings.Contains(name, "arm64") || strings.Contains(name, "aarch64") || strings.Contains(name, "symbols") || strings.Contains(name, "pdb") || strings.Contains(name, "source") || strings.Contains(name, "report") {
			continue
		}

		score := 0
		switch {
		case strings.HasSuffix(name, ".exe"):
			score += 100
		case strings.HasSuffix(name, ".msi"):
			score += 95
		default:
			continue
		}
		if strings.Contains(name, "x64") || strings.Contains(name, "amd64") || strings.Contains(name, "win64") {
			score += 60
		}
		if strings.Contains(name, "installer") || strings.Contains(name, "setup") {
			score += 30
		}
		if strings.Contains(name, "usbip") {
			score += 10
		}
		if score > bestScore {
			bestScore = score
			best = releaseAsset{Name: asset.Name, URL: asset.BrowserDownloadURL, Size: asset.Size}
		}
	}
	if bestScore < 0 {
		return releaseAsset{}, fmt.Errorf("release %s does not contain a Windows x64 EXE or MSI installer; the official release page was opened instead", release.TagName)
	}
	return best, nil
}

func validateGitHubDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid GitHub download URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return errors.New("the driver download is not HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	allowed := host == "github.com" || host == "api.github.com" ||
		host == "objects.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
	if !allowed {
		return fmt.Errorf("refusing unexpected download host: %s", host)
	}
	return nil
}

func verifyAuthenticodeSignature(path string) error {
	systemRoot := os.Getenv("SystemRoot")
	candidates := []string{
		filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "PowerShell", "7", "pwsh.exe"),
	}
	var powershell string
	for _, candidate := range candidates {
		if candidate != "" {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				powershell = candidate
				break
			}
		}
	}
	if powershell == "" {
		return errors.New("Windows PowerShell was not found, so the Authenticode signature cannot be checked")
	}

	escaped := strings.ReplaceAll(path, "'", "''")
	script := "$s=Get-AuthenticodeSignature -LiteralPath '" + escaped + "'; " +
		"Write-Output ('Status=' + $s.Status); " +
		"Write-Output ('Signer=' + $s.SignerCertificate.Subject); " +
		"if ($s.Status -ne 'Valid') { exit 23 }"
	cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	hideCommandWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if guiManager != nil && guiManager.logger != nil {
		guiManager.logger.Printf("Installer Authenticode check: %s", strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", " | "), "\n", " | "))
	}
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return fmt.Errorf("Authenticode status is not Valid: %s", text)
	}
	return nil
}

func startInstallerElevated(path string) error {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve installer path: %w", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return fmt.Errorf("installer file is unavailable: %w", err)
	}
	if info.IsDir() {
		return errors.New("installer path is a directory")
	}
	path = absolutePath

	ext := strings.ToLower(filepath.Ext(path))
	verb := utf16("runas")
	directory := utf16(filepath.Dir(path))

	var file, parameters *uint16
	if ext == ".msi" {
		file = utf16("msiexec.exe")
		parameters = utf16(`/i "` + path + `"`)
	} else {
		file = utf16(path)
		parameters = utf16("")
	}

	r, _, callErr := procShellExecute.Call(
		mainWindow,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(parameters)),
		uintptr(unsafe.Pointer(directory)),
		swShowNormal,
	)
	if r <= 32 {
		// If the explicit elevation verb is unavailable, ask the normal Windows
		// installer association to launch the file. An installer that requires
		// elevation will still trigger Windows UAC itself.
		fallbackResult, _, fallbackErr := procShellExecute.Call(
			mainWindow,
			uintptr(unsafe.Pointer(utf16("open"))),
			uintptr(unsafe.Pointer(utf16(path))),
			0,
			uintptr(unsafe.Pointer(directory)),
			swShowNormal,
		)
		if fallbackResult > 32 {
			return nil
		}
		if fallbackErr != nil && fallbackErr != syscall.Errno(0) {
			return fmt.Errorf("elevated start returned %d; normal start returned %d: %w", r, fallbackResult, fallbackErr)
		}
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("elevated start returned %d: %w; normal start returned %d", r, callErr, fallbackResult)
		}
		return fmt.Errorf("elevated start returned error code %d; normal start returned error code %d", r, fallbackResult)
	}
	return nil
}

func revealDownloadedFile(path string) error {
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		if err != nil {
			return fmt.Errorf("downloaded installer is unavailable: %w", err)
		}
		return errors.New("downloaded installer path is a directory")
	}

	return openDriverDownloadsDirectory()
}

func driverDownloadsDir() string {
	if downloads, err := windowsDownloadsDir(); err == nil && strings.TrimSpace(downloads) != "" {
		return downloads
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, "Downloads")
	}
	return filepath.Join(os.TempDir(), "Downloads")
}

func windowsDownloadsDir() (string, error) {
	var rawPath *uint16
	hr, _, _ := procSHGetKnownFolderPath.Call(
		uintptr(unsafe.Pointer(&folderIDDownloads)),
		0,
		0,
		uintptr(unsafe.Pointer(&rawPath)),
	)
	if int32(hr) < 0 || rawPath == nil {
		return "", fmt.Errorf("SHGetKnownFolderPath failed with HRESULT 0x%08X", uint32(hr))
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(rawPath)))

	characters := make([]uint16, 0, 260)
	for index := uintptr(0); index < 32768; index++ {
		character := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(rawPath)) + index*2))
		if character == 0 {
			break
		}
		characters = append(characters, character)
	}
	path := syscall.UTF16ToString(characters)
	if strings.TrimSpace(path) == "" {
		return "", errors.New("Windows returned an empty Downloads path")
	}
	return filepath.Clean(path), nil
}

func openDriverDownloadsFromGUI() {
	if err := openDriverDownloadsDirectory(); err != nil {
		setStatus("The driver download folder could not be opened.")
		showError("Could not open the driver download folder:\n\n" + driverDownloadsDir() + "\n\n" + err.Error())
		return
	}
	setStatus("Opened Downloads: " + driverDownloadsDir())
}

func openDriverDownloadsDirectory() error {
	directory := driverDownloadsDir()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create driver download folder: %w", err)
	}
	r, _, callErr := procShellExecute.Call(
		mainWindow,
		uintptr(unsafe.Pointer(utf16("open"))),
		uintptr(unsafe.Pointer(utf16(directory))),
		0, 0, swShowNormal,
	)
	if r <= 32 {
		// A direct Explorer start also works on systems where the shell verb is
		// unavailable or restricted by policy.
		explorer := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
		if startErr := exec.Command(explorer, directory).Start(); startErr == nil {
			return nil
		}
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("open Downloads folder returned %d: %w", r, callErr)
		}
		return fmt.Errorf("open Downloads folder returned error code %d", r)
	}
	return nil
}

func openURL(url string) {
	r, _, _ := procShellExecute.Call(
		mainWindow,
		uintptr(unsafe.Pointer(utf16("open"))),
		uintptr(unsafe.Pointer(utf16(url))),
		0, 0, swShowNormal,
	)
	if r <= 32 {
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}

func setStatus(s string) {
	if statusText == 0 {
		return
	}
	procSendMessage.Call(statusText, wmSetText, 0, uintptr(unsafe.Pointer(utf16(s))))
}

func showFatalDialog(message string) {
	procMessageBox.Call(
		0,
		uintptr(unsafe.Pointer(utf16(message))),
		uintptr(unsafe.Pointer(utf16("Virtual Cables - Startup Error"))),
		0x10,
	)
}

func showError(s string) {
	procMessageBox.Call(
		mainWindow,
		uintptr(unsafe.Pointer(utf16(s))),
		uintptr(unsafe.Pointer(utf16("Virtual Cables"))),
		0x10,
	)
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
