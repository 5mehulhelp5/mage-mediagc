// Command mage-mediagc is a standalone garbage collector for Magento 2 catalog
// media and the database rows that outlive deleted products.
package main

import (
	"os"

	"github.com/shuaiZend/mage-mediagc/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
