package backup

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	previewv1 "github.com/documentdb/documentdb-operator/api/preview"
	"github.com/documentdb/documentdb-operator/test/e2e"
	"github.com/documentdb/documentdb-operator/test/e2e/pkg/e2eutils/assertions"
	bkp "github.com/documentdb/documentdb-operator/test/e2e/pkg/e2eutils/backup"
	"github.com/documentdb/documentdb-operator/test/e2e/pkg/e2eutils/documentdb"
	"github.com/documentdb/documentdb-operator/test/e2e/pkg/e2eutils/namespaces"
	"github.com/documentdb/documentdb-operator/test/e2e/pkg/e2eutils/timeouts"
	shareddb "github.com/documentdb/documentdb-operator/test/shared/documentdb"
)

// schemaVersionAnnotation mirrors util.AnnotationSchemaVersion in the
// operator. It is intentionally hard-coded here so this e2e spec pins
// the exact on-the-wire annotation key the restore-validation contract
// depends on — a rename on the operator side must be a conscious,
// two-place change.
const schemaVersionAnnotation = "documentdb.io/schema-version"

// olderBinaryVersion is a semver guaranteed to be lower than any real
// DocumentDB extension schema version (which are 0.1x.y). Restores
// pinned to it are rejected at admission *before* any image pull, so it
// never needs to resolve to a pullable tag.
const olderBinaryVersion = "0.1.0"

// sourceSchemaVersion polls a source DocumentDB's status.schemaVersion
// until it is non-empty, returning the observed value. The operator
// records this on Backups and stamps it onto retained PVs; both restore
// validations compare a restore's binary version against it.
func sourceSchemaVersion(ctx context.Context, c client.Client, key types.NamespacedName) string {
	var observed string
	Eventually(func() string {
		dd, err := shareddb.Get(ctx, c, key)
		if err != nil {
			return ""
		}
		observed = dd.Status.SchemaVersion
		return observed
	}, timeouts.For(timeouts.DocumentDBReady), timeouts.PollInterval(timeouts.DocumentDBReady)).
		ShouldNot(BeEmpty(), "source %s never reported status.schemaVersion", key)
	return observed
}

// pinBinaryVersion forces a restore CR's resolved binary version to v by
// clearing spec.image (whose tag would otherwise win in resolveBinaryVersion)
// and setting spec.documentDBVersion. This makes the admission decision
// deterministic regardless of the DOCUMENTDB_IMAGE the CI job injects.
func pinBinaryVersion(dd *previewv1.DocumentDB, v string) {
	dd.Spec.Image = nil
	dd.Spec.DocumentDBVersion = v
}

var _ = Describe("DocumentDB restore — schema-version compatibility (#434)",
	Label(e2e.BackupLabel, e2e.NeedsCSISnapshotsLabel, e2e.SlowLabel), e2e.MediumLevelLabel,
	func() {
		var (
			ctx context.Context
			ns  string
			c   client.Client
		)

		BeforeEach(func() {
			e2e.SkipUnlessLevel(e2e.Medium)
			ctx = context.Background()
			c = e2e.SuiteEnv().Client
			skipUnlessCSISnapshotsUsable(ctx, c)
			ns = namespaces.NamespaceForSpec(e2e.BackupLabel)
			createNamespace(ctx, c, ns)
			createCredentialSecret(ctx, c, ns)
		})

		It("records the schema version on the Backup and blocks restore onto an older binary", func() {
			const (
				sourceName = "schema-bk-src"
				backupName = "schema-bk-src-backup"
			)

			// Source cluster → Ready, then capture its installed schema version.
			src, err := documentdb.Create(ctx, c, ns, sourceName, documentdb.CreateOptions{
				Base:          "documentdb",
				Vars:          baseVars(sourceName, ns, "2Gi"),
				ManifestsRoot: manifestsRoot(),
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func(ctx SpecContext) { _ = shareddb.Delete(ctx, c, src, 3*time.Minute) })

			srcKey := types.NamespacedName{Namespace: ns, Name: sourceName}
			Eventually(assertions.AssertDocumentDBReady(ctx, c, srcKey),
				timeouts.For(timeouts.DocumentDBReady),
				timeouts.PollInterval(timeouts.DocumentDBReady),
			).Should(Succeed())
			schema := sourceSchemaVersion(ctx, c, srcKey)

			// On-demand Backup → Completed, and the operator must have
			// recorded the source's schema version onto its status.
			_, err = bkp.Create(ctx, c, bkp.BackupVars{
				Name: backupName, Namespace: ns, ClusterName: sourceName, RetentionDays: 1,
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func(ctx SpecContext) { _ = bkp.Delete(ctx, c, ns, backupName, 1*time.Minute) })
			completed, err := bkp.WaitForCompleted(ctx, c, ns, backupName, timeouts.For(timeouts.BackupComplete))
			Expect(err).NotTo(HaveOccurred(), "backup %s/%s did not complete", ns, backupName)
			Expect(completed).NotTo(BeNil())
			Eventually(func() string {
				b, err := bkp.Get(ctx, c, ns, backupName)
				if err != nil {
					return ""
				}
				return b.Status.SchemaVersion
			}, timeouts.For(timeouts.DocumentDBReady), timeouts.PollInterval(timeouts.DocumentDBReady)).
				Should(Equal(schema), "Backup.Status.SchemaVersion must record the source cluster's schema version")

			By("rejecting a restore whose binary version is older than the backup's schema version")
			bad := buildRecoveryDocumentDB(ns, "schema-bk-dst-bad",
				"recovery_from_backup.yaml.template",
				map[string]string{"BACKUP_NAME": backupName})
			pinBinaryVersion(bad, olderBinaryVersion)
			err = c.Create(ctx, bad)
			Expect(err).To(HaveOccurred(), "restore onto an older binary must be rejected at admission")
			Expect(err.Error()).To(ContainSubstring("older than the backup's schema version"))

			By("admitting a restore whose binary version matches the backup's schema version")
			good := buildRecoveryDocumentDB(ns, "schema-bk-dst-good",
				"recovery_from_backup.yaml.template",
				map[string]string{"BACKUP_NAME": backupName})
			pinBinaryVersion(good, schema)
			Expect(c.Create(ctx, good)).To(Succeed(),
				"restore onto a binary >= the backup's schema version must be admitted")
			DeferCleanup(func(ctx SpecContext) { _ = shareddb.Delete(ctx, c, good, 3*time.Minute) })
		})

		It("stamps the schema version on the PV and blocks PV restore onto an older binary", func() {
			const sourceName = "schema-pv-src"

			// Source cluster → Ready; the PV controller stamps the schema
			// version onto the backing PV as an annotation.
			src, err := documentdb.Create(ctx, c, ns, sourceName, documentdb.CreateOptions{
				Base:          "documentdb",
				Vars:          baseVars(sourceName, ns, "2Gi"),
				ManifestsRoot: manifestsRoot(),
			})
			Expect(err).NotTo(HaveOccurred())
			srcKey := types.NamespacedName{Namespace: ns, Name: sourceName}
			Eventually(assertions.AssertDocumentDBReady(ctx, c, srcKey),
				timeouts.For(timeouts.DocumentDBReady),
				timeouts.PollInterval(timeouts.DocumentDBReady),
			).Should(Succeed())
			schema := sourceSchemaVersion(ctx, c, srcKey)

			By("waiting for the PV controller to stamp the schema-version annotation")
			var pvName string
			Eventually(func(g Gomega) {
				pv, err := bkp.FindRetainedPV(ctx, c, ns, sourceName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pv).NotTo(BeNil())
				g.Expect(pv.Annotations).To(HaveKeyWithValue(schemaVersionAnnotation, schema),
					"PV must be annotated with the source's schema version")
				pvName = pv.Name
			}, timeouts.For(timeouts.DocumentDBReady), timeouts.PollInterval(timeouts.DocumentDBReady)).
				Should(Succeed())

			// Delete the source (reclaimPolicy Retain by default) so the
			// annotated PV survives for a restore.
			Expect(shareddb.Delete(ctx, c, src, 3*time.Minute)).To(Succeed())

			By("rejecting a PV restore whose binary version is older than the PV's schema version")
			bad := buildRecoveryDocumentDB(ns, "schema-pv-dst-bad",
				"recovery_from_pv.yaml.template",
				map[string]string{"PV_NAME": pvName})
			pinBinaryVersion(bad, olderBinaryVersion)
			err = c.Create(ctx, bad)
			Expect(err).To(HaveOccurred(), "PV restore onto an older binary must be rejected at admission")
			Expect(err.Error()).To(ContainSubstring("older than the PersistentVolume's schema version"))

			By("admitting a PV restore whose binary version matches the PV's schema version")
			good := buildRecoveryDocumentDB(ns, "schema-pv-dst-good",
				"recovery_from_pv.yaml.template",
				map[string]string{"PV_NAME": pvName})
			pinBinaryVersion(good, schema)
			Expect(c.Create(ctx, good)).To(Succeed(),
				"PV restore onto a binary >= the PV's schema version must be admitted")
			DeferCleanup(func(ctx SpecContext) { _ = shareddb.Delete(ctx, c, good, 3*time.Minute) })
		})
	})
