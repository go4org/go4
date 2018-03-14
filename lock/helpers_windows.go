package lock

import (
	"golang.org/x/sys/windows"
)

func loadSystemDLL(name string) (*windows.DLL, error) {
	const LOAD_LIBRARY_SEARCH_SYSTEM32 = 0x00000800
	modHandle, err := windows.LoadLibraryEx(name, 0, LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return nil, err
	}
	return &windows.DLL{Name: "kernel32", Handle: modHandle}, nil
}

func winCreateEphemeral(name string) (windows.Handle, error) {
	const FILE_ATTRIBUTE_TEMPORARY = 0x100
	const FILE_FLAG_DELETE_ON_CLOSE = 0x04000000

	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(name), windows.GENERIC_WRITE, windows.FILE_SHARE_DELETE|windows.FILE_SHARE_READ, nil, windows.OPEN_ALWAYS, FILE_ATTRIBUTE_TEMPORARY|FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {

		return 0, err
	}
	return handle, nil
}
