/*
 * Copyright The Dragonfly Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dragonflyoss/Dragonfly/pkg/stringutils"
	"github.com/dragonflyoss/Dragonfly/supernode/daemon/mgr/ha"
	"net/http"

	"github.com/dragonflyoss/Dragonfly/apis/types"
	"github.com/dragonflyoss/Dragonfly/pkg/constants"
	"github.com/dragonflyoss/Dragonfly/pkg/errortypes"
	"github.com/dragonflyoss/Dragonfly/pkg/netutils"
	sutil "github.com/dragonflyoss/Dragonfly/supernode/util"

	"github.com/go-openapi/strfmt"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// RegisterResponseData is the data when registering supernode successfully.
type RegisterResponseData struct {
	TaskID     string `json:"taskId"`
	FileLength int64  `json:"fileLength"`
	PieceSize  int32  `json:"pieceSize"`
}

// PullPieceTaskResponseContinueData is the data when successfully pulling piece task
// and the task is continuing.
type PullPieceTaskResponseContinueData struct {
	Range     string `json:"range"`
	PieceNum  int    `json:"pieceNum"`
	PieceSize int32  `json:"pieceSize"`
	PieceMd5  string `json:"pieceMd5"`
	Cid       string `json:"cid"`
	PeerIP    string `json:"peerIp"`
	PeerPort  int    `json:"peerPort"`
	Path      string `json:"path"`
	DownLink  int    `json:"downLink"`
}

var statusMap = map[string]string{
	"700": types.PiecePullRequestDfgetTaskStatusSTARTED,
	"701": types.PiecePullRequestDfgetTaskStatusRUNNING,
	"702": types.PiecePullRequestDfgetTaskStatusFINISHED,
}

var resultMap = map[string]string{
	"500": types.PiecePullRequestPieceResultFAILED,
	"501": types.PiecePullRequestPieceResultSUCCESS,
	"502": types.PiecePullRequestPieceResultINVALID,
	"503": types.PiecePullRequestPieceResultSEMISUC,
}

func (s *Server) registry(ctx context.Context, rw http.ResponseWriter, req *http.Request) (err error) {
	reader := req.Body
	request := &types.TaskRegisterRequest{}
	if err := json.NewDecoder(reader).Decode(request); err != nil {
		return errors.Wrap(errortypes.ErrInvalidValue, err.Error())
	}
	if err := request.Validate(strfmt.NewFormats()); err != nil {
		return errors.Wrap(errortypes.ErrInvalidValue, err.Error())
	}

	peerCreateRequest := &types.PeerCreateRequest{
		IP:       request.IP,
		HostName: strfmt.Hostname(request.HostName),
		Port:     request.Port,
		Version:  request.Version,
		PeerID:   request.PeerID,
	}
	peerCreateResponse, err := s.PeerMgr.Register(ctx, peerCreateRequest)
	if err != nil {
		logrus.Errorf("failed to register peer %+v: %v", peerCreateRequest, err)
		return errors.Wrapf(errortypes.ErrSystemError, "failed to register peer: %v", err)
	}
	logrus.Infof("success to register peer %+v", peerCreateRequest)

	peerID := peerCreateResponse.ID
	taskCreateRequest := &types.TaskCreateRequest{
		CID:         request.CID,
		CallSystem:  request.CallSystem,
		Dfdaemon:    request.Dfdaemon,
		Headers:     netutils.ConvertHeaders(request.Headers),
		Identifier:  request.Identifier,
		Md5:         request.Md5,
		Path:        request.Path,
		PeerID:      peerID,
		RawURL:      request.RawURL,
		TaskURL:     request.TaskURL,
		SupernodeIP: request.SuperNodeIP,
	}
	s.OriginClient.RegisterTLSConfig(taskCreateRequest.RawURL, request.Insecure, request.RootCAs)
	request.PeerID = peerID
	resp, err := s.TaskMgr.Register(ctx, taskCreateRequest, request)
	if err != nil {
		logrus.Errorf("failed to register task %+v: %v", taskCreateRequest, err)
		return err
	}
	//if !request.Trigger{
	//	request.Trigger=true
	//	request.PeerID=peerID
	//	s.HaMgr.SendPostCopy(context.TODO(), request, "/peer/registry", s.Config.OtherSupernodes[0])
	//}

	logrus.Debugf("success to register task %+v", taskCreateRequest)
	return EncodeResponse(rw, http.StatusOK, &types.ResultInfo{
		Code: constants.Success,
		Msg:  constants.GetMsgByCode(constants.Success),
		Data: &RegisterResponseData{
			TaskID:     resp.ID,
			FileLength: resp.FileLength,
			PieceSize:  resp.PieceSize,
		},
	})
}

func (s *Server) pullPieceTask(ctx context.Context, rw http.ResponseWriter, req *http.Request) (err error) {
	params := req.URL.Query()
	taskID := params.Get("taskId")
	srcCID := params.Get("srcCid")
	// try to get dstPID
	dstCID := params.Get("dstCid")
	request := &types.PiecePullRequest{
		DfgetTaskStatus: statusMap[params.Get("status")],
		PieceRange:      params.Get("range"),
		PieceResult:     resultMap[params.Get("result")],
		DstCid:dstCID,
	}

	taskInfo,err:=s.TaskMgr.Get(ctx,taskID)
	if err!=nil{
		return err
	}
	if s.Config.UseHA&&taskInfo.CDNPeerID!=s.Config.GetSuperPID(){
		request.DstPID=s.Config.GetSuperPID()
	}else{
		if !stringutils.IsEmptyStr(dstCID) {
			dstDfgetTask, err := s.DfgetTaskMgr.Get(ctx, dstCID, taskID)
			if err != nil {
				logrus.Warnf("failed to get dfget task by dstCID(%s) and taskID(%s), and the srcCID is %s, err: %v",
					dstCID, taskID, srcCID, err)
			} else {
				request.DstPID = dstDfgetTask.PeerID
			}
		}
	}
	isFinished, data, err := s.TaskMgr.GetPieces(ctx, taskID, srcCID, request)
	if err != nil {
		if errortypes.IsCDNFail(err) {
			logrus.Errorf("taskID:%s, failed to get pieces %+v: %v", taskID, request, err)
		}

		resultInfo := NewResultInfoWithError(err)
		return EncodeResponse(rw, http.StatusOK, &types.ResultInfo{
			Code: int32(resultInfo.code),
			Msg:  resultInfo.msg,
			Data: data,
		})
	}

	if isFinished {
		return EncodeResponse(rw, http.StatusOK, &types.ResultInfo{
			Code: constants.CodePeerFinish,
			Data: data,
		})
	}

	var datas []*PullPieceTaskResponseContinueData
	pieceInfos, ok := data.([]*types.PieceInfo)
	if !ok {
		return EncodeResponse(rw, http.StatusOK, &types.ResultInfo{
			Code: constants.CodeSystemError,
			Msg:  "failed to parse PullPieceTaskResponseContinueData",
		})
	}

	for _, v := range pieceInfos {
		datas = append(datas, &PullPieceTaskResponseContinueData{
			Range:     v.PieceRange,
			PieceNum:  sutil.CalculatePieceNum(v.PieceRange),
			PieceSize: v.PieceSize,
			PieceMd5:  v.PieceMD5,
			Cid:       v.Cid,
			PeerIP:    v.PeerIP,
			PeerPort:  int(v.PeerPort),
			Path:      v.Path,
		})
	}
	return EncodeResponse(rw, http.StatusOK, &types.ResultInfo{
		Code: constants.CodePeerContinue,
		Data: datas,
	})
}

func (s *Server) reportPiece(ctx context.Context, rw http.ResponseWriter, req *http.Request) (err error) {
	var request  *types.PieceUpdateRequest
	params := req.URL.Query()
	taskID := params.Get("taskId")
	srcCID := params.Get("cid")
	dstCID := params.Get("dstCid")
	pieceRange := params.Get("pieceRange")
	taskInfo,err:=s.TaskMgr.Get(ctx,taskID)
	if err!=nil{
		return err
	}
	if s.Config.UseHA&&taskInfo.CDNPeerID!=s.Config.GetSuperPID(){
		request = &types.PieceUpdateRequest{
			ClientID:    srcCID,
			DstPID:      s.Config.GetSuperPID(),
			PieceStatus: types.PieceUpdateRequestPieceStatusSUCCESS,
			DstCID:      dstCID,
			SendCopy:true,
		}
	}else{
		dstDfgetTask, err := s.DfgetTaskMgr.Get(ctx, dstCID, taskID)
		if err != nil {
			return err
		}
		request = &types.PieceUpdateRequest{
			ClientID:    srcCID,
			DstPID:      dstDfgetTask.PeerID,
			PieceStatus: types.PieceUpdateRequestPieceStatusSUCCESS,
			DstCID:      dstCID,
			SendCopy:false,
		}

	}
	if err := s.TaskMgr.UpdatePieceStatus(ctx, taskID, pieceRange, request); err != nil {
		logrus.Errorf("failed to update pieces status %+v: %v", request, err)
		return err
	}

	return EncodeResponse(rw, http.StatusOK, &types.ResultInfo{
		Code: constants.CodeGetPieceReport,
	})
}

func (s *Server) reportServiceDown(ctx context.Context, rw http.ResponseWriter, req *http.Request) (err error) {
	params := req.URL.Query()
	taskID := params.Get("taskId")
	cID := params.Get("cid")

	taskinfo ,err:=s.TaskMgr.Get(ctx,taskID)
	if taskinfo.CDNPeerID!=s.Config.GetSuperPID(){
		var  resp bool
		for _,node :=range s.Config.GetOtherSupernodeInfo(){
			if node.PID==taskinfo.CDNPeerID{
				request:= ha.RpcServerDownRequest{
					TaskID:taskID,
					CID:cID,
				}
				err:=node.RPCClient.Call("RpcManager.RpcDfgetServerDown", request, &resp)
				if err!=nil{
					fmt.Println("####",err)
					logrus.Errorf("failed to send server down request to supernode,err: %v",err)
				}
			}
		}
	}
	dfgetTask, err := s.DfgetTaskMgr.Get(ctx, cID, taskID)
	if err != nil {
		return err
	}

	if err := s.ProgressMgr.DeletePieceProgressByCID(ctx, taskID, cID); err != nil {
		return err
	}

	if err := s.ProgressMgr.DeletePeerStateByPeerID(ctx, dfgetTask.PeerID); err != nil {
		return err
	}

	if err := s.PeerMgr.DeRegister(ctx, dfgetTask.PeerID); err != nil {
		return err
	}

	if err := s.DfgetTaskMgr.Delete(ctx, cID, taskID); err != nil {
		return err
	}

	return EncodeResponse(rw, http.StatusOK, &types.ResultInfo{
		Code: constants.CodeGetPeerDown,
	})
}
