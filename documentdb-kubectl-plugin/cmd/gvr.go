package cmd

import "k8s.io/apimachinery/pkg/runtime/schema"

const (
	backupGVRResource          = "backups"
	scheduledBackupGVRResource = "scheduledbackups"
)

func documentDBGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: documentDBGVRGroup, Version: documentDBGVRVersion, Resource: documentDBGVRResource}
}

func backupGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: documentDBGVRGroup, Version: documentDBGVRVersion, Resource: backupGVRResource}
}

func scheduledBackupGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: documentDBGVRGroup, Version: documentDBGVRVersion, Resource: scheduledBackupGVRResource}
}
