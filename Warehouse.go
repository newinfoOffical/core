/*
File Username:  Warehouse.go
Copyright:  2021 Peernet s.r.o.
Author:     Peter Kleissner
*/

package core

import (
    "github.com/newinfoOffical/core/warehouse"
)

func (backend *Backend) initUserWarehouse() {
    var err error
    backend.UserWarehouse, err = warehouse.Init(backend.Config.WarehouseMain)

    if err != nil {
        backend.LogError("initUserWarehouse", "error: %s\n", err.Error())
    }
}
