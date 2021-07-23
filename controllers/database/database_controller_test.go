package controllers_test

import (
	"context"
	"fmt"
	databasev1 "github.com/bedag/kubernetes-dbaas/apis/database/v1"
	. "github.com/bedag/kubernetes-dbaas/controllers/database"
	. "github.com/bedag/kubernetes-dbaas/pkg/test"
	"github.com/bedag/kubernetes-dbaas/pkg/typeutil"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	k8sError "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"os"
	"path"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"time"
)

const (
	DbMariadbFilename   = "db-mariadb.yaml"
	DbPostgresFilename  = "db-postgres.yaml"
	DbSqlserverFilename = "db-sqlserver.yaml"
	RotateAnnotation    = "dbaas.bedag.ch/rotate"
)

var _ = Describe(FormatTestDesc(E2e, "Database controller"), func() {
	// Define utility constants for object names and testing timeouts/durations and intervals.
	const (
		timeout  = time.Second * 10
		duration = time.Second * 10
		interval = time.Millisecond * 100
	)

	// Prepare Database resources
	postgresDatabaseRes, err := getDbFromTestdata(DbPostgresFilename)
	Expect(err).NotTo(HaveOccurred())
	sqlserverDatabaseRes, err := getDbFromTestdata(DbSqlserverFilename)
	Expect(err).NotTo(HaveOccurred())
	mariadbDatabaseRes, err := getDbFromTestdata(DbMariadbFilename)
	Expect(err).NotTo(HaveOccurred())

	Context("when testing the happy path", func() {
		FContext("when reconciling a PostgreSQL Database resource", func() {
			AfterEach(func() {

			})
			FIt("should handle its lifecycle correctly", func() {
				testDatabaseLifecycleHappyPathWithRotate(postgresDatabaseRes, duration, timeout, interval)
			})
			FIt("should handle user mistakenly deleting a Secret by calling Rotate to regenerate it", func() {
				testSecretDeletedMistakenly(postgresDatabaseRes, duration, timeout, interval)
			})
		})
		Context("when reconciling a MariaDB Database resource", func() {
			It("should handle its lifecycle correctly", func() {
				testDatabaseLifecycleHappyPath(mariadbDatabaseRes, duration, timeout, interval)
			})
		})
		Context("when reconciling a SQLServer Database resource", func() {
			It("should handle its lifecycle correctly", func() {
				testDatabaseLifecycleHappyPath(sqlserverDatabaseRes, duration, timeout, interval)
			})
		})
	})
})

func testDatabaseLifecycleHappyPathWithRotate(db databasev1.Database, duration, timeout, interval interface{}) {
	By("creating the API resource successfully with condition Ready set to true", func() {
		//Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(context.Background(), &db)).Should(Succeed())
		Eventually(func() error {
			return checkDbReady(&db)
		}, timeout, interval).Should(BeNil())
		// We don't just check the Ready condition would be eventually True, we also check that it
		// stays that way for a certain period of time as an additional assurance
		Consistently(func() error {
			return checkDbReady(&db)
		}, duration, interval).Should(BeNil())
	})
	By("creating the relative Secret resource successfully", func() {
		secret := v1.Secret{}
		logf.Log.Info("creating the relative Secret resource successfully")
		Eventually(func() error {
			return k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
				Name: FormatSecretName(&db)}, &secret)
		}, timeout, interval).Should(Succeed())
		// Taken from testdata/db-postgres.yaml
		Expect(secret.Data).To(HaveKeyWithValue("password", []byte("testpassword")))
	})
	By("rotating the credentials", func() {
		// Add rotate annotation
		Expect(k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace, Name: db.Name}, &db))
		db.Annotations = map[string]string{RotateAnnotation: "true"}
		Expect(k8sClient.Update(context.Background(), &db)).Should(Succeed())
		// Check annotation has been removed
		Eventually(func() string {
			newDb := databasev1.Database{}
			_ = k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace, Name: db.Name}, &newDb)
			return newDb.Annotations[RotateAnnotation]
		}, timeout, interval).Should(Equal(""))
		// Check if password was updated
		secret := v1.Secret{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
			Name: FormatSecretName(&db)}, &secret)).Should(Succeed())
		// Rotate recreates the Secret. The Secret should contain the same values as when it is created through the
		// Create operation.
		Expect(secret.Data).To(HaveKey("username"))
		Expect(secret.Data).To(HaveKey("password"))
		Expect(secret.Data).To(HaveKey("dbName"))
		Expect(secret.Data).To(HaveKey("port"))
		Expect(secret.Data).To(HaveKey("server"))
		Expect(secret.Data).To(HaveKey("dsn"))
		// It should also have a new entry to test if the Secret is well updated.
		Expect(string(secret.Data["lastRotation"])).ToNot(Equal(""))
		Expect(secret.Data).To(HaveKey("lastRotation"))
		// Check password was changed succesfully
		Expect(string(secret.Data["password"])).ToNot(Equal(""))
		Expect(string(secret.Data["password"])).ToNot(Equal("testpassword"))
		Eventually(func() error {
			return checkDbReady(&db)
		}, timeout, interval).Should(BeNil())
	})
	By("deleting the API resource successfully", func() {
		err := k8sClient.Delete(context.Background(), &db)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			return k8sError.IsNotFound(checkDbReady(&db))
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			secret := v1.Secret{}
			err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
				Name: FormatSecretName(&db)}, &secret)
			if !isTestEnvUsingExistingCluster() {
			// Envtest does not include garbage collection, therefore Secrets must be deleted manually
				_ = k8sClient.Delete(context.Background(), &secret)
			}
			return k8sError.IsNotFound(err)
		}, timeout, interval).Should(BeTrue())
	})
}

func isTestEnvUsingExistingCluster() bool {
	return os.Getenv("TEST_USE_EXISTING_CLUSTER") == "true"
}

func testDatabaseLifecycleHappyPath(db databasev1.Database, duration, timeout, interval interface{}) {
	By("creating the API resource successfully with condition Ready set to true", func() {
		//Expect(err).NotTo(HaveOccurred())
		err := k8sClient.Create(context.Background(), &db)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() error {
			return checkDbReady(&db)
		}, timeout, interval).Should(BeNil())
		// We don't just check the Ready condition would be eventually True, we also check that it
		// stays that way for a certain period of time as an additional assurance
		Consistently(func() error {
			return checkDbReady(&db)
		}, duration, interval).Should(BeNil())
	})
	By("creating the relative Secret resource successfully", func() {
		Eventually(func() error {
			secret := v1.Secret{}
			return k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
				Name: FormatSecretName(&db)}, &secret)
		}, timeout, interval).Should(BeNil())
		Eventually(func() error {
			return checkDbReady(&db)
		}, timeout, interval).Should(BeNil())
	})
	By("deleting the API resource successfully", func() {
		err := k8sClient.Delete(context.Background(), &db)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			return k8sError.IsNotFound(checkDbReady(&db))
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			secret := v1.Secret{}
			err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
				Name: FormatSecretName(&db)}, &secret)
			if !isTestEnvUsingExistingCluster() {
				// Envtest does not include garbage collection, therefore Secrets must be deleted manually
				_ = k8sClient.Delete(context.Background(), &secret)
			}
			return k8sError.IsNotFound(err)
		}, timeout, interval).Should(BeTrue())
	})
}

func testSecretDeletedMistakenly(db databasev1.Database, duration, timeout, interval interface{}) {
	By("creating the API resource successfully with condition Ready set to true", func() {
		//Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(context.Background(), &db)).Should(Succeed())
		Eventually(func() error {
			return checkDbReady(&db)
		}, timeout, interval).Should(BeNil())
		// We don't just check the Ready condition would be eventually True, we also check that it
		// stays that way for a certain period of time as an additional assurance
		Consistently(func() error {
			return checkDbReady(&db)
		}, duration, interval).Should(BeNil())
	})
	By("creating the relative Secret resource successfully", func() {
		secret := v1.Secret{}
		logf.Log.Info("creating the relative Secret resource successfully")
		Eventually(func() error {
			return k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
				Name: FormatSecretName(&db)}, &secret)
		}, timeout, interval).Should(Succeed())
		// Taken from testdata/db-postgres.yaml
		Expect(secret.Data).To(HaveKeyWithValue("password", []byte("testpassword")))
	})
	By("mistakenly deleting the Secret resource", func() {
		oldSecret := v1.Secret{}
		recreatedSecret := v1.Secret{}
		_ = k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
			Name: FormatSecretName(&db)}, &oldSecret)
		err := k8sClient.Delete(context.Background(), &oldSecret)
		Expect(err).NotTo(HaveOccurred())
		// Eventually, the Secret will be recreated, its password must be different than before
		Eventually(func() error {
			return k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
				Name: FormatSecretName(&db)}, &recreatedSecret)
		}, timeout, interval).Should(BeNil())
		// Secret should be recreated correctly
		Expect(recreatedSecret.Data).To(HaveKey("username"))
		Expect(recreatedSecret.Data).To(HaveKey("password"))
		Expect(recreatedSecret.Data).To(HaveKey("dbName"))
		Expect(recreatedSecret.Data).To(HaveKey("port"))
		Expect(recreatedSecret.Data).To(HaveKey("server"))
		Expect(recreatedSecret.Data).To(HaveKey("dsn"))
		// Password should be rotated
		logf.Log.Info("password before: " + string(oldSecret.Data["password"]))
		logf.Log.Info("password after recreation: " + string(recreatedSecret.Data["password"]))
		Expect(recreatedSecret.Data["password"]).ToNot(Equal(oldSecret.Data["password"]))
		Expect(recreatedSecret.Data["password"]).ToNot(Equal(""))
		Eventually(func() error {
			return checkDbReady(&db)
		}, timeout, interval).Should(BeNil())
	})
	By("deleting the API resource successfully", func() {
		err := k8sClient.Delete(context.Background(), &db)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			return k8sError.IsNotFound(checkDbReady(&db))
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			secret := v1.Secret{}
			err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace,
				Name: FormatSecretName(&db)}, &secret)
			if !isTestEnvUsingExistingCluster() {
				// Envtest does not include garbage collection, therefore Secrets must be deleted manually
				_ = k8sClient.Delete(context.Background(), &secret)
			}
			return k8sError.IsNotFound(err)
		}, timeout, interval).Should(BeTrue())
	})
}

func getDbFromTestdata(filename string) (databasev1.Database, error) {
	return readDbYaml(filename)
}

func readDbYaml(filename string) (databasev1.Database, error) {
	dbFilepath := path.Join(testdataFilepath, filename)
	db := databasev1.Database{}
	dat, err := ioutil.ReadFile(dbFilepath)
	if err != nil {
		return databasev1.Database{}, err
	}
	err = yaml.Unmarshal(dat, &db)
	if err != nil {
		return databasev1.Database{}, err
	}
	return db, nil
}

func checkDbReady(db *databasev1.Database) error {
	// Get a fresh Database resource from the API server
	freshDb := databasev1.Database{}
	err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(db), &freshDb)
	if err != nil {
		return err
	}
	ready := meta.FindStatusCondition(freshDb.Status.Conditions, typeutil.TypeReady)
	if ready == nil {
		return fmt.Errorf("ready condition is nil")
	}
	if ready.Status != metav1.ConditionTrue {
		return fmt.Errorf("database is not ready: %s: %s", ready.Reason, ready.Message)
	}
	return nil
}